package model

type Node struct {
	ID          int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"column:name" json:"name"`
	Secret      string `gorm:"column:secret" json:"secret"`
	Ip          string `gorm:"column:ip" json:"ip"`
	EntryIps    string `gorm:"column:entry_ips" json:"entryIps"`
	ServerIp    string `gorm:"column:server_ip" json:"serverIp"`
	PortSta     int    `gorm:"column:port_sta" json:"portSta"`
	PortEnd     int    `gorm:"column:port_end" json:"portEnd"`
	Version     string `gorm:"column:version" json:"version"`
	Http        int    `gorm:"column:http" json:"http"`
	Tls         int    `gorm:"column:tls" json:"tls"`
	Socks       int    `gorm:"column:socks" json:"socks"`
	XrayEnabled int    `gorm:"column:xray_enabled" json:"vEnabled"`
	XrayVersion string `gorm:"column:xray_version" json:"vVersion"`
	XrayStatus  int    `gorm:"column:xray_status" json:"vStatus"`
	CreatedTime int64  `gorm:"column:created_time" json:"createdTime"`
	UpdatedTime int64  `gorm:"column:updated_time" json:"updatedTime"`
	Status      int    `gorm:"column:status" json:"status"`
	Inx              int    `gorm:"column:inx" json:"inx"`
	GroupName        string `gorm:"column:group_name" json:"groupName"`
	DisguiseName     string `gorm:"column:disguise_name" json:"disguiseName"`
	XrayDisguiseName string `gorm:"column:xray_disguise_name" json:"vDisguiseName"`
}

func (Node) TableName() string {
	return "node"
}
