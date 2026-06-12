// Package repository implements the engine's store.Store interface on top of
// GORM + PostgreSQL, reusing model.Server for host inventory so deployment
// targets never duplicate the Server Management registry.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"df-build-server/internal/deploy/engine/store"
	"df-build-server/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormStore implements engine/store.Store against GORM/PostgreSQL.
type GormStore struct {
	db *gorm.DB
}

// NewGormStore builds a store backed by the given DB handle.
func NewGormStore(db *gorm.DB) *GormStore { return &GormStore{db: db} }

var _ store.Store = (*GormStore)(nil)

// ---- singleton: deployment settings ----

func (s *GormStore) GetDeploymentSettings(ctx context.Context) (*store.DeploymentSettings, error) {
	var row model.DeploymentSettings
	err := s.db.WithContext(ctx).First(&row, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &store.DeploymentSettings{SSHPort: 22, RetainDeployments: 20, DefaultTimeoutSeconds: 1800}, nil
	}
	if err != nil {
		return nil, err
	}
	return &store.DeploymentSettings{
		SSHUser:               row.SSHUser,
		SSHPrivateKeyPath:     row.SSHPrivateKeyPath,
		SSHPort:               row.SSHPort,
		RemoteRoot:            row.RemoteRoot,
		RetainDeployments:     row.RetainDeployments,
		DefaultTimeoutSeconds: row.DefaultTimeoutSeconds,
		UpdatedAt:             row.UpdatedAt,
	}, nil
}

func (s *GormStore) UpdateDeploymentSettings(ctx context.Context, in *store.DeploymentSettings) error {
	row := model.DeploymentSettings{
		ID:                    1,
		SSHUser:               in.SSHUser,
		SSHPrivateKeyPath:     in.SSHPrivateKeyPath,
		SSHPort:               in.SSHPort,
		RemoteRoot:            in.RemoteRoot,
		RetainDeployments:     in.RetainDeployments,
		DefaultTimeoutSeconds: in.DefaultTimeoutSeconds,
		UpdatedAt:             time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

// ---- singleton: network settings ----

func (s *GormStore) GetNetworkSettings(ctx context.Context) (*store.NetworkSettings, error) {
	var row model.DeploymentNetworkSettings
	err := s.db.WithContext(ctx).First(&row, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &store.NetworkSettings{NodeCIDRMaskSize: 24}, nil
	}
	if err != nil {
		return nil, err
	}
	return &store.NetworkSettings{
		VIP:              row.VIP,
		ServiceCIDR:      row.ServiceCIDR,
		ClusterCIDR:      row.ClusterCIDR,
		NodeCIDRMaskSize: row.NodeCIDRMaskSize,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

func (s *GormStore) UpdateNetworkSettings(ctx context.Context, in *store.NetworkSettings) error {
	row := model.DeploymentNetworkSettings{
		ID:               1,
		VIP:              in.VIP,
		ServiceCIDR:      in.ServiceCIDR,
		ClusterCIDR:      in.ClusterCIDR,
		NodeCIDRMaskSize: in.NodeCIDRMaskSize,
		UpdatedAt:        time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

// UpdateGlobalConfig applies the partial PUT atomically in one transaction.
func (s *GormStore) UpdateGlobalConfig(ctx context.Context, u store.GlobalConfigUpdate) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txStore := &GormStore{db: tx}
		if u.Deployment != nil {
			if err := txStore.UpdateDeploymentSettings(ctx, u.Deployment); err != nil {
				return err
			}
		}
		if u.Network != nil {
			if err := txStore.UpdateNetworkSettings(ctx, u.Network); err != nil {
				return err
			}
		}
		if u.EnvReplace {
			if err := txStore.UpsertEnv(ctx, u.Env); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- env (replace-all) ----

func (s *GormStore) ListEnv(ctx context.Context) ([]*store.EnvEntry, error) {
	var rows []model.DeploymentEnvEntry
	if err := s.db.WithContext(ctx).Order("key asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.EnvEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, &store.EnvEntry{Key: r.Key, Value: r.Value, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

func (s *GormStore) UpsertEnv(ctx context.Context, entries []*store.EnvEntry) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&model.DeploymentEnvEntry{}).Error; err != nil {
			return err
		}
		now := time.Now()
		for _, e := range entries {
			if e == nil {
				continue
			}
			row := model.DeploymentEnvEntry{Key: e.Key, Value: e.Value, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *GormStore) DeleteEnv(ctx context.Context, key string) error {
	return s.db.WithContext(ctx).Delete(&model.DeploymentEnvEntry{Key: key}).Error
}

// ---- enabled components (ordered) ----

func (s *GormStore) ListEnabledComponents(ctx context.Context) ([]*store.EnabledComponent, error) {
	var rows []model.DeploymentEnabledComponent
	if err := s.db.WithContext(ctx).Order("position asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.EnabledComponent, 0, len(rows))
	for _, r := range rows {
		out = append(out, &store.EnabledComponent{Name: r.Name, Position: r.Position, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

func (s *GormStore) ReplaceEnabledComponents(ctx context.Context, names []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&model.DeploymentEnabledComponent{}).Error; err != nil {
			return err
		}
		now := time.Now()
		for i, n := range names {
			row := model.DeploymentEnabledComponent{Name: n, Position: i, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- hosts: backed by model.Server ----

func serverToHost(srv *model.Server) *store.HostSpec {
	name := srv.Remark
	if name == "" {
		name = srv.Host
	}
	return &store.HostSpec{
		ID:        int64(srv.ID),
		Name:      name,
		Address:   srv.Host,
		Metadata:  map[string]string{},
		CreatedAt: srv.CreatedAt,
		UpdatedAt: srv.UpdatedAt,
	}
}

func (s *GormStore) ListHosts(ctx context.Context) ([]*store.HostSpec, error) {
	var rows []model.Server
	if err := s.db.WithContext(ctx).Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.HostSpec, 0, len(rows))
	for i := range rows {
		out = append(out, serverToHost(&rows[i]))
	}
	return out, nil
}

func (s *GormStore) GetHost(ctx context.Context, id int64) (*store.HostSpec, error) {
	var row model.Server
	err := s.db.WithContext(ctx).First(&row, uint(id)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return serverToHost(&row), nil
}

// Host mutation is owned by Server Management; deployment management does not
// create or edit hosts. These satisfy the interface but reject writes.
var errHostsManagedElsewhere = errors.New("deploy: hosts are managed via server management")

func (s *GormStore) CreateHost(ctx context.Context, h *store.HostSpec) error {
	return errHostsManagedElsewhere
}

func (s *GormStore) UpdateHost(ctx context.Context, h *store.HostSpec) error {
	return errHostsManagedElsewhere
}

func (s *GormStore) DeleteHost(ctx context.Context, id int64) error {
	return errHostsManagedElsewhere
}

func (s *GormStore) CountTargetsByHost(ctx context.Context, hostID int64) (int64, []string, error) {
	var components []string
	if err := s.db.WithContext(ctx).Model(&model.DeploymentComponentTarget{}).
		Where("server_id = ?", uint(hostID)).
		Distinct().Pluck("component_name", &components).Error; err != nil {
		return 0, nil, err
	}
	sort.Strings(components)
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.DeploymentComponentTarget{}).
		Where("server_id = ?", uint(hostID)).Count(&count).Error; err != nil {
		return 0, nil, err
	}
	return count, components, nil
}

// ---- component targets ----

func (s *GormStore) ListAllTargets(ctx context.Context) ([]*store.ComponentTargets, error) {
	var rows []model.DeploymentComponentTarget
	if err := s.db.WithContext(ctx).Order("component_name asc, server_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	return groupTargets(rows), nil
}

func (s *GormStore) GetTargets(ctx context.Context, component string) (*store.ComponentTargets, error) {
	var rows []model.DeploymentComponentTarget
	if err := s.db.WithContext(ctx).Where("component_name = ?", component).
		Order("server_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	ct := &store.ComponentTargets{ComponentName: component, HostIDs: []int64{}}
	seen := map[int64]bool{}
	for _, r := range rows {
		id := int64(r.ServerID)
		if !seen[id] {
			seen[id] = true
			ct.HostIDs = append(ct.HostIDs, id)
		}
	}
	return ct, nil
}

func (s *GormStore) ReplaceTargets(ctx context.Context, component string, hostIDs []int64) error {
	return s.ReplaceTargetsForOwner(ctx, component, "", hostIDs)
}

func (s *GormStore) ReplaceTargetsForOwner(ctx context.Context, component, ownerVC string, hostIDs []int64) error {
	return s.ReplaceTargetsForOwners(ctx, []store.OwnerComponentHosts{{Component: component, OwnerVC: ownerVC, HostIDs: hostIDs}})
}

func (s *GormStore) ReplaceTargetsForOwners(ctx context.Context, batch []store.OwnerComponentHosts) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for _, b := range batch {
			if err := tx.Where("component_name = ? AND owner_vc = ?", b.Component, b.OwnerVC).
				Delete(&model.DeploymentComponentTarget{}).Error; err != nil {
				return err
			}
			seen := map[int64]bool{}
			for _, hid := range b.HostIDs {
				if seen[hid] {
					continue
				}
				seen[hid] = true
				row := model.DeploymentComponentTarget{
					ComponentName: b.Component,
					OwnerVC:       b.OwnerVC,
					ServerID:      uint(hid),
					CreatedAt:     now,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *GormStore) ListTargetsForOwner(ctx context.Context, ownerVC string) (map[string][]int64, error) {
	var rows []model.DeploymentComponentTarget
	if err := s.db.WithContext(ctx).Where("owner_vc = ?", ownerVC).
		Order("component_name asc, server_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]int64{}
	for _, r := range rows {
		out[r.ComponentName] = append(out[r.ComponentName], int64(r.ServerID))
	}
	return out, nil
}

func (s *GormStore) ListLegacyOwnerTargets(ctx context.Context) ([]*store.ComponentTargets, error) {
	var rows []model.DeploymentComponentTarget
	if err := s.db.WithContext(ctx).Where("owner_vc = ?", "").
		Order("component_name asc, server_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	return groupTargets(rows), nil
}

func groupTargets(rows []model.DeploymentComponentTarget) []*store.ComponentTargets {
	byComp := map[string]*store.ComponentTargets{}
	order := []string{}
	for _, r := range rows {
		ct, ok := byComp[r.ComponentName]
		if !ok {
			ct = &store.ComponentTargets{ComponentName: r.ComponentName, HostIDs: []int64{}}
			byComp[r.ComponentName] = ct
			order = append(order, r.ComponentName)
		}
		ct.HostIDs = append(ct.HostIDs, int64(r.ServerID))
	}
	out := make([]*store.ComponentTargets, 0, len(order))
	for _, name := range order {
		out = append(out, byComp[name])
	}
	return out
}

// ---- component deploy state ----

func (s *GormStore) GetComponentDeployState(ctx context.Context, component string) (*store.ComponentDeployState, error) {
	var row model.DeploymentComponentState
	err := s.db.WithContext(ctx).First(&row, "component_name = ?", component).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &store.ComponentDeployState{ComponentName: component, Status: store.DeployStateNotDeployed}, nil
	}
	if err != nil {
		return nil, err
	}
	return &store.ComponentDeployState{
		ComponentName:    row.ComponentName,
		Status:           row.Status,
		LastDeploymentID: row.LastDeploymentID,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

func (s *GormStore) ListComponentDeployStates(ctx context.Context) ([]*store.ComponentDeployState, error) {
	var rows []model.DeploymentComponentState
	if err := s.db.WithContext(ctx).Order("component_name asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.ComponentDeployState, 0, len(rows))
	for _, r := range rows {
		out = append(out, &store.ComponentDeployState{
			ComponentName:    r.ComponentName,
			Status:           r.Status,
			LastDeploymentID: r.LastDeploymentID,
			UpdatedAt:        r.UpdatedAt,
		})
	}
	return out, nil
}

func (s *GormStore) SetComponentDeployState(ctx context.Context, component, status string, deploymentID int64) error {
	row := model.DeploymentComponentState{
		ComponentName:    component,
		Status:           status,
		LastDeploymentID: deploymentID,
		UpdatedAt:        time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "component_name"}},
		UpdateAll: true,
	}).Create(&row).Error
}

// ---- overrides ----

func (s *GormStore) ListOverrides(ctx context.Context) ([]*store.ComponentOverride, error) {
	var rows []model.DeploymentComponentOverride
	if err := s.db.WithContext(ctx).Order("component_name asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.ComponentOverride, 0, len(rows))
	for _, r := range rows {
		o, err := overrideToStore(&r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *GormStore) GetOverride(ctx context.Context, name string) (*store.ComponentOverride, error) {
	var row model.DeploymentComponentOverride
	err := s.db.WithContext(ctx).First(&row, "component_name = ?", name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return overrideToStore(&row)
}

func (s *GormStore) UpsertOverride(ctx context.Context, o *store.ComponentOverride) error {
	data, err := json.Marshal(o.Params)
	if err != nil {
		return err
	}
	row := model.DeploymentComponentOverride{
		ComponentName: o.ComponentName,
		ParamsJSON:    string(data),
		UpdatedAt:     time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "component_name"}},
		UpdateAll: true,
	}).Create(&row).Error
}

func (s *GormStore) DeleteOverride(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Delete(&model.DeploymentComponentOverride{ComponentName: name}).Error
}

func overrideToStore(r *model.DeploymentComponentOverride) (*store.ComponentOverride, error) {
	params := map[string]any{}
	if r.ParamsJSON != "" {
		if err := json.Unmarshal([]byte(r.ParamsJSON), &params); err != nil {
			return nil, err
		}
	}
	return &store.ComponentOverride{ComponentName: r.ComponentName, Params: params, UpdatedAt: r.UpdatedAt}, nil
}

// ---- deployments ----

func (s *GormStore) CreateDeployment(ctx context.Context, d *store.Deployment) error {
	row := deploymentToModel(d)
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	d.ID = int64(row.ID)
	d.CreatedAt = row.CreatedAt
	return nil
}

func (s *GormStore) GetDeployment(ctx context.Context, id int64) (*store.Deployment, error) {
	var row model.Deployment
	err := s.db.WithContext(ctx).First(&row, uint(id)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return modelToDeployment(&row), nil
}

func (s *GormStore) UpdateDeployment(ctx context.Context, d *store.Deployment) error {
	row := deploymentToModel(d)
	row.ID = uint(d.ID)
	return s.db.WithContext(ctx).Save(&row).Error
}

func (s *GormStore) ListDeployments(ctx context.Context, f store.DeploymentFilter) ([]*store.Deployment, error) {
	q := s.applyDeploymentFilter(s.db.WithContext(ctx), f).Order("id desc")
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}
	var rows []model.Deployment
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.Deployment, 0, len(rows))
	for i := range rows {
		out = append(out, modelToDeployment(&rows[i]))
	}
	return out, nil
}

func (s *GormStore) CountDeployments(ctx context.Context, f store.DeploymentFilter) (int64, error) {
	var count int64
	err := s.applyDeploymentFilter(s.db.WithContext(ctx).Model(&model.Deployment{}), f).Count(&count).Error
	return count, err
}

func (s *GormStore) applyDeploymentFilter(q *gorm.DB, f store.DeploymentFilter) *gorm.DB {
	if f.Component != "" {
		q = q.Where("target_component = ?", f.Component)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	return q
}

func (s *GormStore) PruneOldDeployments(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	var ids []uint
	if err := s.db.WithContext(ctx).Model(&model.Deployment{}).
		Order("id desc").Offset(keep).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.db.WithContext(ctx).Where("deployment_id IN ?", ids).
		Delete(&model.DeploymentRunLog{}).Error; err != nil {
		return 0, err
	}
	res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.Deployment{})
	return res.RowsAffected, res.Error
}

func deploymentToModel(d *store.Deployment) model.Deployment {
	return model.Deployment{
		TaskType:        d.TaskType,
		TargetComponent: d.TargetComponent,
		TargetHost:      d.TargetHost,
		DryRun:          d.DryRun,
		Status:          d.Status,
		StartedAt:       d.StartedAt,
		EndedAt:         d.EndedAt,
		ErrorSummary:    d.ErrorSummary,
		ScopeKind:       d.ScopeKind,
		Phase:           d.Phase,
		DurationMS:      d.DurationMS,
		RunDir:          d.RunDir,
	}
}

func modelToDeployment(r *model.Deployment) *store.Deployment {
	return &store.Deployment{
		ID:              int64(r.ID),
		TaskType:        r.TaskType,
		TargetComponent: r.TargetComponent,
		TargetHost:      r.TargetHost,
		DryRun:          r.DryRun,
		Status:          r.Status,
		StartedAt:       r.StartedAt,
		EndedAt:         r.EndedAt,
		ErrorSummary:    r.ErrorSummary,
		ScopeKind:       r.ScopeKind,
		Phase:           r.Phase,
		DurationMS:      r.DurationMS,
		RunDir:          r.RunDir,
		CreatedAt:       r.CreatedAt,
	}
}

// ---- deployment logs ----

func (s *GormStore) AppendDeploymentLog(ctx context.Context, log *store.DeploymentLog) error {
	if log.Sequence == 0 {
		var maxSeq int64
		s.db.WithContext(ctx).Model(&model.DeploymentRunLog{}).
			Where("deployment_id = ?", log.DeploymentID).
			Select("COALESCE(MAX(sequence), 0)").Scan(&maxSeq)
		log.Sequence = maxSeq + 1
	}
	if log.Timestamp.IsZero() {
		log.Timestamp = time.Now()
	}
	row := model.DeploymentRunLog{
		DeploymentID: log.DeploymentID,
		Sequence:     log.Sequence,
		Timestamp:    log.Timestamp,
		Component:    log.Component,
		Host:         log.Host,
		Phase:        log.Phase,
		ActionName:   log.ActionName,
		ActionType:   log.ActionType,
		Status:       log.Status,
		Detail:       log.Detail,
		IsError:      log.IsError,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	log.ID = int64(row.ID)
	return nil
}

func (s *GormStore) ListDeploymentLogs(ctx context.Context, deploymentID, afterSeq int64, limit int) ([]*store.DeploymentLog, error) {
	q := s.db.WithContext(ctx).
		Where("deployment_id = ? AND sequence > ?", deploymentID, afterSeq).
		Order("sequence asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []model.DeploymentRunLog
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return logsToStore(rows), nil
}

func (s *GormStore) GetDeploymentLogsTail(ctx context.Context, deploymentID int64, n int) ([]*store.DeploymentLog, error) {
	var rows []model.DeploymentRunLog
	q := s.db.WithContext(ctx).Where("deployment_id = ?", deploymentID).Order("sequence desc")
	if n > 0 {
		q = q.Limit(n)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	// reverse to ascending
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return logsToStore(rows), nil
}

func logsToStore(rows []model.DeploymentRunLog) []*store.DeploymentLog {
	out := make([]*store.DeploymentLog, 0, len(rows))
	for _, r := range rows {
		out = append(out, &store.DeploymentLog{
			ID:           int64(r.ID),
			DeploymentID: r.DeploymentID,
			Sequence:     r.Sequence,
			Timestamp:    r.Timestamp,
			Component:    r.Component,
			Host:         r.Host,
			Phase:        r.Phase,
			ActionName:   r.ActionName,
			ActionType:   r.ActionType,
			Status:       r.Status,
			Detail:       r.Detail,
			IsError:      r.IsError,
		})
	}
	return out
}

// Close is a no-op: the underlying *gorm.DB is owned by the application.
func (s *GormStore) Close() error { return nil }
