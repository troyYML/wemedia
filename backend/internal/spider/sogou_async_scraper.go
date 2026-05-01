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

// SogouAsyncScraper 搜狗异步批量爬虫
type SogouAsyncScraper struct {
	*SogouScraper
	mu       sync.Mutex
	progress models.Progress
}

// NewSogouAsyncScraper 创建搜狗异步爬虫
func NewSogouAsyncScraper(minIntervalSec, maxIntervalSec int) *SogouAsyncScraper {
	return &SogouAsyncScraper{
		SogouScraper: NewSogouScraper(minIntervalSec, maxIntervalSec),
	}
}

// BatchSearchAsync 批量关键词搜索
func (s *SogouAsyncScraper) BatchSearchAsync(
	ctx context.Context,
	config models.SogouSearchConfig,
	progressChan chan<- models.Progress,
	statusChan chan<- models.AccountStatus,
) ([]models.Article, error) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancelFunc = cancel
	defer cancel()

	var allResults []models.Article

	totalKeywords := len(config.Keywords)

	for ki, keyword := range config.Keywords {
		select {
		case <-ctx.Done():
			logger.Log.Warn("搜狗批量搜索被取消")
			return allResults, ctx.Err()
		default:
		}

		logger.Log.Info("开始搜狗搜索关键词", zap.String("keyword", keyword))

		// 发送状态：正在搜索
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName: keyword,
				Status:      "searching",
				Message:     "正在搜狗微信搜索...",
			}
		}

		var keywordArticles []models.Article

		// 遍历页码
		for page := 1; page <= config.MaxPages; page++ {
			select {
			case <-ctx.Done():
				logger.Log.Warn("搜狗搜索被取消", zap.String("keyword", keyword))
				return allResults, ctx.Err()
			default:
			}

			articles, err := s.SearchArticles(ctx, keyword, page)
			if err != nil {
				if strings.Contains(err.Error(), "验证码") || strings.Contains(err.Error(), "反爬虫") {
					logger.Log.Error("搜狗搜索触发验证码", zap.String("keyword", keyword), zap.Int("page", page))
					if statusChan != nil {
						statusChan <- models.AccountStatus{
							AccountName: keyword,
							Status:      "error",
							Message:     err.Error(),
						}
					}
					keywordArticles = nil
					break
				}
				logger.Log.Warn("搜狗搜索失败", zap.String("keyword", keyword), zap.Int("page", page), zap.Error(err))
				break
			}

			if len(articles) == 0 {
				logger.Log.Info("搜狗搜索第N页为空", zap.String("keyword", keyword), zap.Int("page", page))
				break
			}

			logger.Log.Info("搜狗搜索获取文章", zap.String("keyword", keyword), zap.Int("page", page), zap.Int("count", len(articles)))

			keywordArticles = append(keywordArticles, articles...)

			// 发送进度更新
			if progressChan != nil {
				progressChan <- models.Progress{
					Type:    models.ProgressTypeArticle,
					Current: page,
					Total:   config.MaxPages,
					Message: fmt.Sprintf("正在搜索 [%s] (%d/%d页)", keyword, page, config.MaxPages),
				}
			}
		}

		// 发送获取到的文章数
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName:  keyword,
				Status:       "fetching",
				Message:      fmt.Sprintf("已获取 %d 篇文章", len(keywordArticles)),
				ArticleCount: len(keywordArticles),
			}
		}

		// 日期过滤
		if config.StartDate != "" && config.EndDate != "" {
			beforeFilter := len(keywordArticles)
			keywordArticles = s.FilterArticlesByDate(keywordArticles, config.StartDate, config.EndDate)
			logger.Log.Info("搜狗搜索日期过滤", zap.String("keyword", keyword), zap.String("start", config.StartDate), zap.String("end", config.EndDate), zap.Int("before", beforeFilter), zap.Int("after", len(keywordArticles)))
		}

		// 串行获取文章内容
		if config.IncludeContent {
			if statusChan != nil {
				statusChan <- models.AccountStatus{
					AccountName:  keyword,
					Status:       "content",
					Message:      fmt.Sprintf("正在获取文章内容 (%d 篇)...", len(keywordArticles)),
					ArticleCount: len(keywordArticles),
				}
			}

			logger.Log.Info("开始获取搜狗文章内容", zap.String("keyword", keyword), zap.Int("total", len(keywordArticles)))

			successCount := 0
			for i := range keywordArticles {
				select {
				case <-ctx.Done():
					logger.Log.Warn("获取搜狗文章内容被取消", zap.String("keyword", keyword))
					return allResults, ctx.Err()
				default:
				}

				// 先解析重定向获取真实微信链接
				realLink, err := s.resolveRedirect(ctx, keywordArticles[i].Link)
				if err != nil {
					logger.Log.Warn("解析搜狗链接重定向失败", zap.String("keyword", keyword), zap.String("link", keywordArticles[i].Link), zap.Error(err))
					// 如果是反爬虫触发，不再尝试获取内容，直接跳过该文章
					if strings.Contains(err.Error(), "反爬虫") {
						logger.Log.Warn("搜狗链接被反爬虫拦截，跳过该文章", zap.String("keyword", keyword), zap.String("title", keywordArticles[i].Title))
						continue
					}
					realLink = keywordArticles[i].Link
				} else {
					keywordArticles[i].Link = realLink
				}

				// 获取文章内容
				content, imageLinks, cleanContent, err := s.GetArticleContent(ctx, realLink)
				if err == nil {
					keywordArticles[i].Content = content
					keywordArticles[i].ImageLinks = imageLinks
					keywordArticles[i].CleanContent = cleanContent
					successCount++
					logger.Log.Info("获取搜狗文章内容成功", zap.String("keyword", keyword), zap.Int("progress", i+1), zap.Int("total", len(keywordArticles)), zap.String("title", keywordArticles[i].Title))
				} else {
					logger.Log.Warn("获取搜狗文章内容失败", zap.String("keyword", keyword), zap.Int("progress", i+1), zap.Int("total", len(keywordArticles)), zap.String("title", keywordArticles[i].Title), zap.Error(err))
					// 如果是反爬虫触发，停止获取该关键词的所有后续文章
					if strings.Contains(err.Error(), "反爬虫") {
						logger.Log.Warn("搜狗获取内容触发反爬虫，停止当前关键词后续文章获取", zap.String("keyword", keyword))
						if statusChan != nil {
							statusChan <- models.AccountStatus{
								AccountName:  keyword,
								Status:       "error",
								Message:      "搜狗触发反爬虫验证，已停止获取",
								ArticleCount: len(keywordArticles),
							}
						}
						break
					}
				}

				// 发送进度更新
				if progressChan != nil {
					progressChan <- models.Progress{
						Type:    models.ProgressTypeContent,
						Current: i + 1,
						Total:   len(keywordArticles),
						Message: fmt.Sprintf("正在获取文章内容 [%s] (%d/%d)", keyword, i+1, len(keywordArticles)),
					}
				}

				// 更新状态
				if statusChan != nil {
					statusChan <- models.AccountStatus{
						AccountName:  keyword,
						Status:       "content",
						Message:      fmt.Sprintf("正在获取文章内容 (%d/%d)", i+1, len(keywordArticles)),
						ArticleCount: len(keywordArticles),
						Progress: &models.ProgressInfo{
							Current: i + 1,
							Total:   len(keywordArticles),
						},
					}
				}
			}

			logger.Log.Info("搜狗文章内容获取完成", zap.String("keyword", keyword), zap.Int("success", successCount), zap.Int("failed", len(keywordArticles)-successCount), zap.Int("total", len(keywordArticles)))
		}

		// 发送完成状态
		if statusChan != nil {
			statusChan <- models.AccountStatus{
				AccountName:  keyword,
				Status:       "completed",
				Message:      "搜索完成",
				ArticleCount: len(keywordArticles),
			}
		}

		logger.Log.Info("搜狗关键词搜索完成", zap.String("keyword", keyword), zap.Int("count", len(keywordArticles)))

		allResults = append(allResults, keywordArticles...)

		// 发送整体进度
		if progressChan != nil {
			progressChan <- models.Progress{
				Type:    models.ProgressTypeAccount,
				Current: ki + 1,
				Total:   totalKeywords,
				Message: fmt.Sprintf("正在处理关键词 (%d/%d)", ki+1, totalKeywords),
			}
		}
	}

	logger.Log.Info("搜狗搜索全部完成", zap.Int("keywords", len(config.Keywords)), zap.Int("articles", len(allResults)))

	return allResults, nil
}

// GetProgress 获取进度
func (s *SogouAsyncScraper) GetProgress() models.Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress
}
