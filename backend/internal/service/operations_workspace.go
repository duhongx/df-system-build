package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrWorkspaceConflict = errors.New("工作区已在其他页面更新，当前草稿已保留，请选择读取服务器版本或覆盖保存")

type WorkspaceDraft struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	DatabaseID     uint   `json:"databaseId"`
	Revision       uint   `json:"revision"`
	Target         string `json:"target"`
	Schema         string `json:"schema"`
	SQL            string `json:"sql"`
	Parameters     string `json:"parameters"`
	MaxRows        int    `json:"maxRows"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
type DatabaseWorkspaceState struct {
	Version        int              `json:"version"`
	Tabs           []WorkspaceDraft `json:"tabs"`
	ClosedTabs     []WorkspaceDraft `json:"closedTabs"`
	ActiveTabID    int64            `json:"activeTabId"`
	DatabaseID     uint             `json:"databaseId"`
	Pane           string           `json:"pane"`
	EditorHeight   int              `json:"editorHeight"`
	NavigationOpen bool             `json:"navigationOpen"`
}
type DatabaseWorkspaceResult struct {
	State     DatabaseWorkspaceState `json:"state"`
	Revision  uint                   `json:"revision"`
	UpdatedAt *time.Time             `json:"updatedAt"`
}

func (s *OperationsService) DatabaseWorkspace(ctx context.Context, owner string) (*DatabaseWorkspaceResult, error) {
	if owner == "" {
		return nil, fmt.Errorf("未取得平台用户身份")
	}
	var row model.DatabaseWorkspace
	out := &DatabaseWorkspaceResult{State: DatabaseWorkspaceState{Version: 1, Tabs: []WorkspaceDraft{}, ClosedTabs: []WorkspaceDraft{}, Pane: "sql", EditorHeight: 245, NavigationOpen: true}}
	err := s.db.WithContext(ctx).Where("owner=?", owner).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(row.StateJSON), &out.State); err != nil {
		return nil, fmt.Errorf("保存的工作区无法读取: %w", err)
	}
	out.Revision = row.Revision
	out.UpdatedAt = &row.UpdatedAt
	return out, nil
}
func (s *OperationsService) SaveDatabaseWorkspace(ctx context.Context, owner string, state DatabaseWorkspaceState, revision uint) (*DatabaseWorkspaceResult, error) {
	if owner == "" {
		return nil, fmt.Errorf("未取得平台用户身份")
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("不支持的工作区版本")
	}
	ids := map[int64]bool{}
	for _, tabs := range [][]WorkspaceDraft{state.Tabs, state.ClosedTabs} {
		for _, tab := range tabs {
			if tab.ID < 1 || ids[tab.ID] {
				return nil, fmt.Errorf("工作区窗口编号无效或重复")
			}
			ids[tab.ID] = true
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	row := model.DatabaseWorkspace{Owner: owner, StateJSON: string(data), Revision: revision + 1, UpdatedAt: time.Now()}
	var result *gorm.DB
	if revision == 0 {
		result = s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	} else {
		result = s.db.WithContext(ctx).Model(&model.DatabaseWorkspace{}).Where("owner=? AND revision=?", owner, revision).Updates(map[string]any{"state_json": string(data), "revision": revision + 1, "updated_at": row.UpdatedAt})
	}
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrWorkspaceConflict
	}
	return &DatabaseWorkspaceResult{State: state, Revision: row.Revision, UpdatedAt: &row.UpdatedAt}, nil
}
func (s *OperationsService) SavedDatabaseQueries(ctx context.Context, owner string) ([]model.DatabaseSavedQuery, error) {
	out := []model.DatabaseSavedQuery{}
	err := s.db.WithContext(ctx).Where("owner=?", owner).Order("updated_at DESC,id DESC").Find(&out).Error
	return out, err
}
func (s *OperationsService) SaveDatabaseQuery(ctx context.Context, owner string, row model.DatabaseSavedQuery) (*model.DatabaseSavedQuery, error) {
	row.Owner = owner
	row.Name = strings.TrimSpace(row.Name)
	if owner == "" || row.Name == "" || strings.TrimSpace(row.SQL) == "" {
		return nil, fmt.Errorf("请填写 SQL 标题和内容")
	}
	if len([]rune(row.Name)) > 128 {
		return nil, fmt.Errorf("标题最多 128 个字符")
	}
	var parameters []any
	if row.ParametersJSON == "" {
		row.ParametersJSON = "[]"
	}
	if err := json.Unmarshal([]byte(row.ParametersJSON), &parameters); err != nil || parameters == nil {
		return nil, fmt.Errorf("绑定参数必须是 JSON 数组")
	}
	if row.DatabaseID != 0 {
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.OperationsDatabase{}).Where("id=?", row.DatabaseID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, fmt.Errorf("收藏对应的连接已移除，请重新选择适用范围")
		}
	}
	if row.ID == 0 {
		row.Revision = 1
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return nil, err
		}
		return &row, nil
	}
	result := s.db.WithContext(ctx).Model(&model.DatabaseSavedQuery{}).Where("id=? AND owner=? AND revision=?", row.ID, owner, row.Revision).Updates(map[string]any{"name": row.Name, "database_id": row.DatabaseID, "schema": row.Schema, "sql": row.SQL, "parameters_json": row.ParametersJSON, "revision": row.Revision + 1, "updated_at": time.Now()})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("收藏不存在或已被其他页面修改，请刷新后重试")
	}
	if err := s.db.WithContext(ctx).Where("id=? AND owner=?", row.ID, owner).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *OperationsService) DeleteSavedDatabaseQuery(ctx context.Context, owner string, id, revision uint) error {
	r := s.db.WithContext(ctx).Where("id=? AND owner=? AND revision=?", id, owner, revision).Delete(&model.DatabaseSavedQuery{})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("收藏不存在或已变化，请刷新后重试")
	}
	return nil
}
