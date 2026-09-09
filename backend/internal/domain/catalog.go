package domain

import "time"

const (
	VendorOpenAI    = "openai"
	VendorAnthropic = "anthropic"
	VendorGrok      = "grok"
	VendorZhipu     = "zhipu"
	VendorMoonshot  = "moonshot"
	VendorDeepseek  = "deepseek"
)

// CatalogModel is one chat model synced from the public models.dev library.
type CatalogModel struct {
	ID         uint      `gorm:"primaryKey" json:"-"`
	Vendor     string    `gorm:"size:32;uniqueIndex:idx_catalog_vendor_model;not null" json:"vendor"`
	ModelID    string    `gorm:"size:128;uniqueIndex:idx_catalog_vendor_model;not null" json:"id"`
	Name       string    `gorm:"size:256" json:"name"`
	Protocol    string    `gorm:"size:16;not null" json:"protocol"`
	InputCost   float64   `gorm:"type:decimal(16,8)" json:"input_cost"`
	OutputCost  float64   `gorm:"type:decimal(16,8)" json:"output_cost"`
	ReleaseDate string    `gorm:"size:16" json:"release_date,omitempty"`
	SyncedAt    time.Time `json:"synced_at"`
}

func (m CatalogModel) Cost() float64 {
	return m.InputCost + m.OutputCost
}

// CatalogMeta is the singleton row recording the last models.dev sync.
type CatalogMeta struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Source     string     `gorm:"size:256" json:"source"`
	ModelCount int        `json:"model_count"`
	SyncedAt   *time.Time `json:"synced_at"`
	LastError  string     `gorm:"type:text" json:"last_error,omitempty"`
}

// ProbeVendor describes one vendor that can be selected as a deep-probe model.
type ProbeVendor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

func ProbeVendors() []ProbeVendor {
	return []ProbeVendor{
		{ID: VendorOpenAI, Name: "OpenAI", Protocol: ProtocolOpenAI},
		{ID: VendorAnthropic, Name: "Anthropic", Protocol: ProtocolAnthropic},
		{ID: VendorGrok, Name: "Grok", Protocol: ProtocolOpenAI},
		{ID: VendorZhipu, Name: "智谱", Protocol: ProtocolOpenAI},
		{ID: VendorMoonshot, Name: "月之暗面", Protocol: ProtocolOpenAI},
		{ID: VendorDeepseek, Name: "Deepseek", Protocol: ProtocolOpenAI},
	}
}

func ValidProbeVendor(id string) bool {
	for _, v := range ProbeVendors() {
		if v.ID == id {
			return true
		}
	}
	return false
}

func VendorProtocol(vendor string) string {
	if vendor == VendorAnthropic {
		return ProtocolAnthropic
	}
	return ProtocolOpenAI
}

func ProbeVendorIDs() []string {
	vs := ProbeVendors()
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}
