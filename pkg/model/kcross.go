package model

import (
	"errors"

	"gorm.io/gorm"
)

// ErrKcrossBindingNotFound 删除绑定时集群无绑定
var ErrKcrossBindingNotFound = errors.New("kcross binding not found")

// KcrossSetting kcross Gateway 集成配置（单行表，ID 恒为 1）。
// 独立于 GeneralSetting，避免侵入上游既有模型
type KcrossSetting struct {
	Model
	Enabled    bool         `json:"enabled" gorm:"column:enabled;type:boolean;not null;default:false"`
	GatewayURL string       `json:"gatewayUrl" gorm:"column:gateway_url;type:varchar(500)"`
	AdminToken SecretString `json:"-" gorm:"column:admin_token;type:text"`
}

// KcrossBinding 集群 ↔ kcross peer 能力绑定：集群本体走原有 Cluster 表与
// API（kubeconfig 模式），本表只记录"该集群经哪个 peer 的哪个能力接入"
type KcrossBinding struct {
	Model
	ClusterID  uint   `json:"clusterId" gorm:"column:cluster_id;uniqueIndex;not null"`
	PeerID     string `json:"peerId" gorm:"column:peer_id;type:varchar(64);index;not null"`
	Capability string `json:"capability" gorm:"column:capability;type:varchar(100);not null"`
}

// GetKcrossSetting 读取集成配置；首行不存在时落一条空配置
func GetKcrossSetting() (*KcrossSetting, error) {
	var setting KcrossSetting
	err := DB.First(&setting, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		setting = KcrossSetting{}
		if err := DB.Create(&setting).Error; err != nil {
			return nil, err
		}
		return &setting, nil
	}
	if err != nil {
		return nil, err
	}
	return &setting, nil
}

// UpdateKcrossSetting 按列更新集成配置
func UpdateKcrossSetting(updates map[string]interface{}) (*KcrossSetting, error) {
	if _, err := GetKcrossSetting(); err != nil {
		return nil, err
	}
	if err := DB.Model(&KcrossSetting{}).Where("id = ?", 1).Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetKcrossSetting()
}

// GetKcrossBindingByCluster 查询集群绑定；无绑定返回 nil
func GetKcrossBindingByCluster(clusterID uint) *KcrossBinding {
	var binding KcrossBinding
	if err := DB.Where("cluster_id = ?", clusterID).First(&binding).Error; err != nil {
		return nil
	}
	return &binding
}

// UpsertKcrossBinding 创建或更新集群绑定
func UpsertKcrossBinding(clusterID uint, peerID, capability string) (*KcrossBinding, error) {
	binding := GetKcrossBindingByCluster(clusterID)
	if binding == nil {
		binding = &KcrossBinding{
			ClusterID:  clusterID,
			PeerID:     peerID,
			Capability: capability,
		}
		if err := DB.Create(binding).Error; err != nil {
			return nil, err
		}
		return binding, nil
	}
	if err := DB.Model(binding).Updates(map[string]interface{}{
		"peer_id":    peerID,
		"capability": capability,
	}).Error; err != nil {
		return nil, err
	}
	binding.PeerID = peerID
	binding.Capability = capability
	return binding, nil
}

// DeleteKcrossBinding 删除集群绑定；无绑定时报 not found
func DeleteKcrossBinding(clusterID uint) error {
	result := DB.Where("cluster_id = ?", clusterID).Delete(&KcrossBinding{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrKcrossBindingNotFound
	}
	return nil
}

// ListKcrossBindings 全量绑定（发现页与集群列表徽标用）
func ListKcrossBindings() ([]*KcrossBinding, error) {
	var bindings []*KcrossBinding
	if err := DB.Find(&bindings).Error; err != nil {
		return nil, err
	}
	return bindings, nil
}
