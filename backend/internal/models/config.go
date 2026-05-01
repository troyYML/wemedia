package models

// Config 应用配置
type Config struct {
	MaxPages           int    `json:"maxPages"`           // 最大页数
	RequestInterval    int    `json:"requestInterval"`    // 兼容旧字段（秒）
	RequestIntervalMin int    `json:"requestIntervalMin"` // 请求间隔下限（秒）
	RequestIntervalMax int    `json:"requestIntervalMax"` // 请求间隔上限（秒）
	MaxWorkers         int    `json:"maxWorkers"`         // 最大并发数
	IncludeContent     bool   `json:"includeContent"`     // 是否获取正文
	CacheExpireHours   int    `json:"cacheExpireHours"`   // 缓存过期时间
	OutputDir          string `json:"outputDir"`          // 输出目录
}

// ScrapeConfig 爬取配置
type ScrapeConfig struct {
	Accounts           []string `json:"accounts"`           // 公众号列表
	StartDate          string   `json:"startDate"`          // 开始日期 YYYY-MM-DD
	EndDate            string   `json:"endDate"`            // 结束日期 YYYY-MM-DD
	RecentDays         int      `json:"recentDays"`         // 采集最近N天（定时任务用，优先于 StartDate/EndDate）
	MaxPages           int      `json:"maxPages"`           // 最大页数
	RequestInterval    int      `json:"requestInterval"`    // 兼容旧字段（秒）
	RequestIntervalMin int      `json:"requestIntervalMin"` // 请求间隔下限（秒）
	RequestIntervalMax int      `json:"requestIntervalMax"` // 请求间隔上限（秒）
	IncludeContent     bool     `json:"includeContent"`     // 是否获取正文
	KeywordFilter      string   `json:"keywordFilter"`      // 关键词过滤
	MaxWorkers         int      `json:"maxWorkers"`         // 最大并发数
}

// SogouSearchConfig 搜狗搜索配置
type SogouSearchConfig struct {
	Keywords           []string `json:"keywords"`           // 搜索关键词列表
	MaxPages           int      `json:"maxPages"`           // 每个关键词最大搜索页数
	RequestIntervalMin int      `json:"requestIntervalMin"` // 请求间隔下限（秒）
	RequestIntervalMax int      `json:"requestIntervalMax"` // 请求间隔上限（秒）
	IncludeContent     bool     `json:"includeContent"`     // 是否获取正文
	StartDate          string   `json:"startDate"`          // 开始日期
	EndDate            string   `json:"endDate"`            // 结束日期
}
