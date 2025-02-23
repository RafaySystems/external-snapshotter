/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package snapshotter

import (
	"context"
	"fmt"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/ptypes"
	csirpc "github.com/kubernetes-csi/csi-lib-utils/rpc"

	"google.golang.org/grpc"

	"k8s.io/api/core/v1"
	"k8s.io/klog"
)

// Snapshotter implements CreateSnapshot/DeleteSnapshot operations against a remote CSI driver.
type Snapshotter interface {
	// CreateSnapshot creates a snapshot for a volume
	CreateSnapshot(ctx context.Context, snapshotName string, volume *v1.PersistentVolume, parameters map[string]string, snapshotterCredentials map[string]string) (driverName string, snapshotId string, timestamp time.Time, size int64, readyToUse bool, err error)

	// DeleteSnapshot deletes a snapshot from a volume
	DeleteSnapshot(ctx context.Context, snapshotID string, snapshotterCredentials map[string]string) (err error)

	// GetSnapshotStatus returns if a snapshot is ready to use, creation time, and restore size.
	GetSnapshotStatus(ctx context.Context, snapshotID string) (bool, time.Time, int64, error)
}

type snapshot struct {
	conn *grpc.ClientConn
}

func NewSnapshotter(conn *grpc.ClientConn) Snapshotter {
	return &snapshot{
		conn: conn,
	}
}

func (s *snapshot) CreateSnapshot(ctx context.Context, snapshotName string, volume *v1.PersistentVolume, parameters map[string]string, snapshotterCredentials map[string]string) (string, string, time.Time, int64, bool, error) {
	klog.V(5).Infof("CSI CreateSnapshot: %s", snapshotName)
	if volume.Spec.CSI == nil {
		return "", "", time.Time{}, 0, false, fmt.Errorf("CSIPersistentVolumeSource not defined in spec")
	}

	client := csi.NewControllerClient(s.conn)

	driverName, err := csirpc.GetDriverName(ctx, s.conn)
	if err != nil {
		return "", "", time.Time{}, 0, false, err
	}

	req := csi.CreateSnapshotRequest{
		SourceVolumeId: volume.Spec.CSI.VolumeHandle,
		Name:           snapshotName,
		Parameters:     parameters,
		Secrets:        snapshotterCredentials,
	}

	rsp, err := client.CreateSnapshot(ctx, &req)
	if err != nil {
		return "", "", time.Time{}, 0, false, err
	}

	klog.V(5).Infof("CSI CreateSnapshot: %s driver name [%s] snapshot ID [%s] time stamp [%d] size [%d] readyToUse [%v]", snapshotName, driverName, rsp.Snapshot.SnapshotId, rsp.Snapshot.CreationTime, rsp.Snapshot.SizeBytes, rsp.Snapshot.ReadyToUse)
	creationTime, err := ptypes.Timestamp(rsp.Snapshot.CreationTime)
	if err != nil {
		return "", "", time.Time{}, 0, false, err
	}
	return driverName, rsp.Snapshot.SnapshotId, creationTime, rsp.Snapshot.SizeBytes, rsp.Snapshot.ReadyToUse, nil
}

func (s *snapshot) DeleteSnapshot(ctx context.Context, snapshotID string, snapshotterCredentials map[string]string) (err error) {
	client := csi.NewControllerClient(s.conn)

	req := csi.DeleteSnapshotRequest{
		SnapshotId: snapshotID,
		Secrets:    snapshotterCredentials,
	}

	if _, err := client.DeleteSnapshot(ctx, &req); err != nil {
		return err
	}

	return nil
}

func (s *snapshot) isListSnapshotsSupported(ctx context.Context) (bool, error) {
	client := csi.NewControllerClient(s.conn)
	capRsp, err := client.ControllerGetCapabilities(ctx, &csi.ControllerGetCapabilitiesRequest{})
	if err != nil {
		return false, err
	}

	for _, cap := range capRsp.Capabilities {
		if cap.GetRpc().GetType() == csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS {
			return true, nil
		}
	}

	return false, nil
}

func (s *snapshot) GetSnapshotStatus(ctx context.Context, snapshotID string) (bool, time.Time, int64, error) {
	client := csi.NewControllerClient(s.conn)

	// If the driver does not support ListSnapshots, assume the snapshot ID is valid.
	listSnapshotsSupported, err := s.isListSnapshotsSupported(ctx)
	if err != nil {
		return false, time.Time{}, 0, fmt.Errorf("failed to check if ListSnapshots is supported: %s", err.Error())
	}
	if !listSnapshotsSupported {
		return true, time.Time{}, 0, nil
	}
	req := csi.ListSnapshotsRequest{
		SnapshotId: snapshotID,
	}
	rsp, err := client.ListSnapshots(ctx, &req)
	if err != nil {
		return false, time.Time{}, 0, err
	}

	if rsp.Entries == nil || len(rsp.Entries) == 0 {
		return false, time.Time{}, 0, fmt.Errorf("can not find snapshot for snapshotID %s", snapshotID)
	}

	creationTime, err := ptypes.Timestamp(rsp.Entries[0].Snapshot.CreationTime)
	if err != nil {
		return false, time.Time{}, 0, err
	}
	return rsp.Entries[0].Snapshot.ReadyToUse, creationTime, rsp.Entries[0].Snapshot.SizeBytes, nil
}
