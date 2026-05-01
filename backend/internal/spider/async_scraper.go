package spider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"WeMediaSpider/backend/internal/models"
	"WeMediaSpider/backend/pkg/logger"

	"go.uber.org/zap"
)

// AsyncScraper 异步爬虫（所有 API 调用通过 RateLimiter 串行化）
type AsyncScraper struct {
	*Scraper
	maxWorkers int // 保留字段兼容，实际已不用于并发控制
	mu         sync.Mutex
	progress   models.Progress
}

// NewAsyncScraper 创建异步爬虫
func NewAsyncScraper(token string, headers map[string]string, maxWorkers int, minIntervalSec int, maxIntervalSec int) *AsyncScraper {
	return &AsyncScraper{
		Scraper:    NewScraperWithInterval(token, headers, minIntervalSec, maxIntervalSec),
		maxWorkers: maxWorkers,
	}
}

// BatchScrapeAsync 串行批量爬取
// 所有 API 请求通过 RateLimiter 串行队列执行，确保每次请求间强制等待 [min, max] 秒间隔
func (as *AsyncScraper) BatchScrapeAsync(
	ctx context.Context,
	config models.ScrapeConfig,
	progressChan chan<- models.Progress,
	statusChan chan<- models.AccountStatus,
) ([]models.Article, error) {
	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(ctx)
	as.cancelFunc = cancel
	defer cancel()

	var allResults []models.Article

	// 串行处理每个公众号
	for _, accountName := range config.Accounts {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			logger.Log.Warn("爬取被取消")
			return allResults, ctx.Err()
		default:
		}

		logger.Log.Info("开始处理公众号", zap.String("account", accountName))

		// 发送状态：正在搜索
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName: accountName,
				Status:      "searching",
				Message:     "正在搜索公众号...",
			}
		}

		// 搜索公众号（通过 RateLimiter 串行化）
		logger.Log.Info("正在搜索公众号", zap.String("account", accountName))
		accounts, err := as.SearchAccount(accountName)
		if err != nil || len(accounts) == 0 {
			logger.Log.Error("未找到公众号", zap.String("account", accountName), zap.Error(err))
			if statusChan != nil {
				statusChan <- models.AccountStatus{
					AccountName: accountName,
					Status:      "error",
					Message:     "未找到公众号",
				}
			}
			continue // 继续处理下一个公众号
		}

		account := accounts[0]
		logger.Log.Info("找到公众号", zap.String("account", accountName), zap.String("fakeid", account.Fakeid), zap.String("alias", account.Alias))

		// 发送状态：正在获取文章列表
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName: accountName,
				Status:      "fetching",
				Message:     "正在获取文章列表...",
			}
		}

		// 串行获取文章列表（通过 RateLimiter 串行化）
		logger.Log.Info("开始获取文章列表", zap.String("account", accountName), zap.Int("maxPages", config.MaxPages))

		var allArticles []models.Article
		for page := 0; page < config.MaxPages; page++ {
			// 检查上下文是否已取消
			select {
			case <-ctx.Done():
				logger.Log.Warn("爬取被取消", zap.String("account", accountName))
				return allResults, ctx.Err()
			default:
			}

			// 获取文章列表（通过 RateLimiter 串行化）
			articles, err := as.GetArticlesList(ctx, account.Fakeid, page)
			if err != nil {
				// 如果是频率限制错误，清空已获取的数据并停止
				if strings.Contains(err.Error(), "freq control") || strings.Contains(err.Error(), "频率") {
					logger.Log.Error("遇到频率限制，清空数据并停止爬取", zap.String("account", accountName), zap.Int("page", page+1))
					if statusChan != nil {
						statusChan <- models.AccountStatus{
							AccountName: accountName,
							Status:      "error",
							Message:     "遇到频率限制，请稍后重试",
						}
					}
					allArticles = nil
					break
				}

				logger.Log.Warn("获取文章列表失败", zap.String("account", accountName), zap.Int("page", page+1), zap.Error(err))
				break
			}

			if len(articles) == 0 {
				logger.Log.Info("第N页为空，停止获取", zap.Int("page", page+1), zap.String("account", accountName))
				break
			}

			logger.Log.Info("获取到文章", zap.String("account", accountName), zap.Int("page", page+1), zap.Int("maxPages", config.MaxPages), zap.Int("count", len(articles)))

			// 设置公众号名称
			for i := range articles {
				articles[i].AccountName = accountName
				articles[i].AccountFakeid = account.Fakeid
			}

			allArticles = append(allArticles, articles...)
		}

		logger.Log.Info("总共获取文章", zap.String("account", accountName), zap.Int("count", len(allArticles)))

		// 打印所有文章链接
		logger.Log.Info("文章链接列表", zap.String("account", accountName))
		for i, article := range allArticles {
			logger.Log.Info("article", zap.Int("index", i+1), zap.String("link", article.Link))
			logger.Log.Info("article title", zap.String("title", article.Title))
		}

		// 日期过滤
		if config.StartDate != "" && config.EndDate != "" {
			beforeFilter := len(allArticles)
			allArticles = as.FilterArticlesByDate(allArticles, config.StartDate, config.EndDate)
			logger.Log.Info("日期过滤", zap.String("account", accountName), zap.String("start", config.StartDate), zap.String("end", config.EndDate), zap.Int("before", beforeFilter), zap.Int("after", len(allArticles)))

			if beforeFilter != len(allArticles) {
				logger.Log.Info("过滤后的文章列表", zap.String("account", accountName))
				for i, article := range allArticles {
					logger.Log.Info("filtered article", zap.Int("index", i+1), zap.String("title", article.Title), zap.String("publishTime", article.PublishTime))
				}
			}
		}

		// 发送获取到的文章数
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName:  accountName,
				Status:       "fetching",
				Message:      fmt.Sprintf("已获取 %d 篇文章", len(allArticles)),
				ArticleCount: len(allArticles),
			}
		}

		// 串行获取文章内容（通过 RateLimiter 串行化）
		if config.IncludeContent || config.KeywordFilter != "" {
			if statusChan != nil {
				statusChan <- models.AccountStatus{
					AccountName:  accountName,
					Status:       "content",
					Message:      fmt.Sprintf("正在串行获取文章内容 (%d 篇)...", len(allArticles)),
					ArticleCount: len(allArticles),
				}
			}

			logger.Log.Info("开始串行获取文章内容", zap.String("account", accountName), zap.Int("total", len(allArticles)))

			successCount := 0
			for i := range allArticles {
				// 检查上下文是否已取消
				select {
				case <-ctx.Done():
					logger.Log.Warn("获取文章内容被取消", zap.String("account", accountName))
					return allResults, ctx.Err()
				default:
				}

				// 串行获取文章内容（通过 RateLimiter 串行化）
				content, imageLinks, cleanContent, err := as.GetArticleContent(ctx, allArticles[i].Link)

				if err == nil {
					allArticles[i].Content = content
					allArticles[i].ImageLinks = imageLinks
					allArticles[i].CleanContent = cleanContent
					successCount++
					logger.Log.Info("获取文章内容成功", zap.String("account", accountName), zap.Int("progress", i+1), zap.Int("total", len(allArticles)), zap.String("title", allArticles[i].Title), zap.Int("length", len(content)))
				} else {
					logger.Log.Warn("获取文章内容失败", zap.String("account", accountName), zap.Int("progress", i+1), zap.Int("total", len(allArticles)), zap.String("title", allArticles[i].Title), zap.Error(err))
				}

				// 发送进度更新
				if progressChan != nil {
					progressChan <- models.Progress{
						Type:    models.ProgressTypeContent,
						Current: i + 1,
						Total:   len(allArticles),
						Message: fmt.Sprintf("正在获取文章内容 [%s] (%d/%d)", accountName, i+1, len(allArticles)),
					}
				}

				// 更新账号状态
				if statusChan != nil {
					statusChan <- models.AccountStatus{
						AccountName:  accountName,
						Status:       "content",
						Message:      fmt.Sprintf("正在获取文章内容 (%d/%d)", i+1, len(allArticles)),
						ArticleCount: len(allArticles),
						Progress: &models.ProgressInfo{
							Current: i + 1,
							Total:   len(allArticles),
						},
					}
				}
			}

			logger.Log.Info("文章内容获取完成", zap.String("account", accountName), zap.Int("success", successCount), zap.Int("failed", len(allArticles)-successCount), zap.Int("total", len(allArticles)))
		}

		// 关键词过滤（在获取正文内容之后进行，以便搜索全文）
		if config.KeywordFilter != "" {
			beforeFilter := len(allArticles)
			allArticles = as.FilterArticlesByKeyword(allArticles, config.KeywordFilter)
			logger.Log.Info("关键词过滤", zap.String("account", accountName), zap.String("keyword", config.KeywordFilter), zap.Int("before", beforeFilter), zap.Int("after", len(allArticles)))

			if beforeFilter != len(allArticles) {
				logger.Log.Info("关键词过滤后的文章列表", zap.String("account", accountName))
				for i, article := range allArticles {
					logger.Log.Info("keyword filtered article", zap.Int("index", i+1), zap.String("title", article.Title))
				}
			}
		}

		// 发送完成状态
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName:  accountName,
				Status:       "completed",
				Message:      "爬取完成",
				ArticleCount: len(allArticles),
			}
		}

		logger.Log.Info("公众号爬取完成", zap.String("account", accountName), zap.Int("count", len(allArticles)))

		allResults = append(allResults, allArticles...)
	}

	logger.Log.Info("所有公众号爬取完成", zap.Int("accounts", len(config.Accounts)), zap.Int("articles", len(allResults)))

	return allResults, nil
}

// GetProgress 获取进度
func (as *AsyncScraper) GetProgress() models.Progress {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.progress
}
