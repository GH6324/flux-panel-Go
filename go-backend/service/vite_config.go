package service

import (
	"fmt"
	"log"
	"strings"
	"time"

	"flux-panel/go-backend/dto"
	"flux-panel/go-backend/model"
)

// publicConfigKeys defines config keys safe for unauthenticated access.
var publicConfigKeys = map[string]bool{
	"captcha_enabled": true,
	"app_name":        true,
}

func GetConfigs() dto.R {
	var configs []model.ViteConfig
	DB.Find(&configs)

	configMap := make(map[string]string)
	for _, c := range configs {
		configMap[c.Name] = c.Value
	}
	return dto.Ok(configMap)
}

// GetPublicConfigs returns only whitelisted config keys for unauthenticated access.
func GetPublicConfigs() dto.R {
	var configs []model.ViteConfig
	DB.Find(&configs)

	configMap := make(map[string]string)
	for _, c := range configs {
		if publicConfigKeys[c.Name] {
			configMap[c.Name] = c.Value
		}
	}
	return dto.Ok(configMap)
}

func GetConfigByName(name string) dto.R {
	if name == "" {
		return dto.Err("配置名称不能为空")
	}

	var cfg model.ViteConfig
	if err := DB.Where("name = ?", name).First(&cfg).Error; err != nil {
		return dto.Err("配置不存在")
	}
	return dto.Ok(cfg)
}

// GetPublicConfigByName returns a config only if it's in the public whitelist.
func GetPublicConfigByName(name string) dto.R {
	if name == "" {
		return dto.Err("配置名称不能为空")
	}
	if !publicConfigKeys[name] {
		return dto.Err("配置不存在")
	}

	var cfg model.ViteConfig
	if err := DB.Where("name = ?", name).First(&cfg).Error; err != nil {
		return dto.Err("配置不存在")
	}
	return dto.Ok(cfg)
}

func UpdateConfigs(configMap map[string]string) dto.R {
	if len(configMap) == 0 {
		return dto.Err("配置数据不能为空")
	}

	var errors []string
	for name, value := range configMap {
		if name == "" {
			continue
		}
		if err := updateOrCreateConfig(name, value); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errors) > 0 {
		return dto.Err("部分配置更新失败: " + strings.Join(errors, "; "))
	}
	return dto.Ok("配置更新成功")
}

func UpdateSingleConfig(name, value string) dto.R {
	if name == "" {
		return dto.Err("配置名称不能为空")
	}
	if value == "" {
		return dto.Err("配置值不能为空")
	}

	if err := updateOrCreateConfig(name, value); err != nil {
		return dto.Err("配置更新失败: " + err.Error())
	}
	return dto.Ok("配置更新成功")
}

func updateOrCreateConfig(name, value string) error {
	var cfg model.ViteConfig
	result := DB.Where("name = ?", name).First(&cfg)

	if result.Error == nil {
		cfg.Value = value
		cfg.Time = time.Now().UnixMilli()
		if err := DB.Save(&cfg).Error; err != nil {
			log.Printf("[ConfigUpdate] 更新配置 %s 失败: %v", name, err)
			return err
		}
	} else {
		cfg = model.ViteConfig{
			Name:  name,
			Value: value,
			Time:  time.Now().UnixMilli(),
		}
		if err := DB.Create(&cfg).Error; err != nil {
			log.Printf("[ConfigUpdate] 创建配置 %s 失败: %v", name, err)
			return err
		}
	}
	return nil
}
