package spider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"WeMediaSpider/backend/internal/models"
	"WeMediaSpider/backend/pkg/logger"
	"WeMediaSpider/backend/pkg/utils"

	"go.uber.org/zap"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
)

// SogouScraper 搜狗微信搜索爬虫
type SogouScraper struct {
	client        *http.Client // 不跟随重定向的客户端，用于 resolveRedirect
	contentClient *http.Client // 跟随重定向的客户端，用于获取文章内容
	converter     *md.Converter
	rateLimiter   *utils.RateLimiter
	cookies       []*http.Cookie
	cancelFunc    context.CancelFunc
	mu            sync.Mutex
}

// NewSogouScraper 创建搜狗爬虫
func NewSogouScraper(minIntervalSec, maxIntervalSec int) *SogouScraper {
	minInterval, maxInterval := normalizeIntervalRange(minIntervalSec, maxIntervalSec)
	rateLimiter := utils.NewRateLimiter(minInterval, maxInterval, 15)

	return &SogouScraper{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		contentClient: &http.Client{
			Timeout: 30 * time.Second,
			// 默认跟随重定向，用于获取文章实际内容
		},
		converter:   md.NewConverter("", true, nil),
		rateLimiter: rateLimiter,
		cookies:     make([]*http.Cookie, 0),
	}
}

// SearchArticles 搜狗微信搜索文章
func (s *SogouScraper) SearchArticles(ctx context.Context, keyword string, page int) ([]models.Article, error) {
	s.rateLimiter.Wait()

	searchURL := fmt.Sprintf("https://weixin.sogou.com/weixin?type=2&s_from=input&query=%s&page=%d",
		url.QueryEscape(keyword), page)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		s.rateLimiter.RecordFailure()
		return nil, err
	}

	req.Header.Set("User-Agent", utils.GetRandomUserAgent())
	req.Header.Set("Referer", "https://weixin.sogou.com/")
	req.Header.Set("Host", "weixin.sogou.com")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// 不显式设置 Accept-Encoding，让 Go 的 http.Client 自动处理 gzip 解压
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	// 携带已有 cookies
	s.mu.Lock()
	for _, cookie := range s.cookies {
		req.AddCookie(cookie)
	}
	s.mu.Unlock()

	resp, err := s.client.Do(req)
	if err != nil {
		s.rateLimiter.RecordFailure()
		return nil, err
	}
	defer resp.Body.Close()

	// 从响应中更新 cookies
	if newCookies := resp.Cookies(); len(newCookies) > 0 {
		s.mu.Lock()
		s.cookies = mergeCookies(s.cookies, newCookies)
		s.mu.Unlock()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.rateLimiter.RecordFailure()
		return nil, err
	}

	bodyStr := string(body)

	// 检测验证码
	if strings.Contains(bodyStr, "请输入验证码") || strings.Contains(bodyStr, "验证码") {
		s.rateLimiter.RecordFailure()
		return nil, fmt.Errorf("搜狗搜索触发验证码，请稍后重试")
	}
	if strings.Contains(resp.Request.URL.String(), "antispider") {
		s.rateLimiter.RecordFailure()
		return nil, fmt.Errorf("搜狗搜索触发反爬虫验证")
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(bodyStr))
	if err != nil {
		s.rateLimiter.RecordFailure()
		return nil, err
	}

	var articles []models.Article

	// 搜索结果列表选择器
	doc.Find(".news-list li .txt-box").Each(func(i int, box *goquery.Selection) {
		article := s.parseSearchResult(box)
		if article.Title != "" {
			article.Source = "sogou"
			articles = append(articles, article)
		}
	})

	// 备选选择器
	if len(articles) == 0 {
		doc.Find(".news-list > li").Each(func(i int, li *goquery.Selection) {
			article := s.parseSearchResult(li)
			if article.Title != "" {
				article.Source = "sogou"
				articles = append(articles, article)
			}
		})
	}

	s.rateLimiter.RecordSuccess()
	return articles, nil
}

// parseSearchResult 解析单个搜索结果
func (s *SogouScraper) parseSearchResult(selection *goquery.Selection) models.Article {
	var article models.Article

	// 标题和链接
	titleLink := selection.Find("h3 a")
	article.Title = strings.TrimSpace(titleLink.Text())
	if href, exists := titleLink.Attr("href"); exists {
		if strings.HasPrefix(href, "/") {
			article.Link = "https://weixin.sogou.com" + href
		} else {
			article.Link = href
		}
	}

	// 摘要
	article.Digest = strings.TrimSpace(selection.Find("p.txt-info").Text())

	// 公众号名称
	article.AccountName = strings.TrimSpace(selection.Find("div.s-p a").Text())

	// 发布时间：尝试从 script 中解析 timeConvert
	timeScript := selection.Find("div.s-p script").Text()
	if timeScript != "" {
		re := regexp.MustCompile(`timeConvert\('([^']+)'\)`)
		matches := re.FindStringSubmatch(timeScript)
		if len(matches) > 1 {
			if ts, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
				article.PublishTimestamp = ts
				article.PublishTime = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
			} else {
				article.PublishTime = matches[1]
			}
		}
	}

	// 备选：从 div.s-p 的 t 属性获取时间戳
	if article.PublishTimestamp == 0 {
		selection.Find("div.s-p").Each(func(i int, sp *goquery.Selection) {
			if t, exists := sp.Attr("t"); exists && t != "" {
				if ts, err := strconv.ParseInt(t, 10, 64); err == nil {
					article.PublishTimestamp = ts
					article.PublishTime = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
				}
			}
		})
	}

	article.CreatedAt = time.Now()
	return article
}

// resolveRedirect 解析搜狗链接重定向，获取真实微信 URL
func (s *SogouScraper) resolveRedirect(ctx context.Context, sogouLink string) (string, error) {
	// 处理相对路径
	if strings.HasPrefix(sogouLink, "/") {
		sogouLink = "https://weixin.sogou.com" + sogouLink
	}
	req, err := http.NewRequestWithContext(ctx, "GET", sogouLink, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", utils.GetRandomUserAgent())
	req.Header.Set("Referer", "https://weixin.sogou.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	s.mu.Lock()
	for _, cookie := range s.cookies {
		req.AddCookie(cookie)
	}
	s.mu.Unlock()

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 从 302 响应的 Location header 获取真实微信 URL
	if location := resp.Header.Get("Location"); location != "" {
		// 检查 Location 是否指向 antispider 页面
		if strings.Contains(location, "antispider") {
			return "", fmt.Errorf("搜狗触发反爬虫验证，链接被重定向到 antispider 页面")
		}
		return location, nil
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	bodyStr := string(body)

	// 检测 antispider 页面
	if strings.Contains(bodyStr, "antispider") {
		return "", fmt.Errorf("搜狗触发反爬虫验证，链接被重定向到 antispider 页面")
	}

	// 尝试从响应 HTML 中解析 var url = '...' 模式
	re := regexp.MustCompile(`var\s+url\s*=\s*['"]([^'"]+)['"]`)
	matches := re.FindStringSubmatch(bodyStr)
	if len(matches) > 1 {
		return matches[1], nil
	}

	// 尝试从 meta refresh 解析: <meta http-equiv="refresh" content="0;url=...">
	metaRe := regexp.MustCompile(`(?i)<meta[^>]+http-equiv=["']?refresh["']?[^>]+content=["']?\d+;\s*url=([^"'\s>]+)`)
	metaMatches := metaRe.FindStringSubmatch(bodyStr)
	if len(metaMatches) > 1 {
		return metaMatches[1], nil
	}

	// 尝试从 <a href="..."> 解析（搜狗 "The URL has moved" 页面）
	linkRe := regexp.MustCompile(`(?i)<a[^>]+href=["']([^"']+)["']`)
	linkMatches := linkRe.FindStringSubmatch(bodyStr)
	if len(linkMatches) > 1 {
		href := linkMatches[1]
		// 如果是 antispider 链接，报错
		if strings.Contains(href, "antispider") {
			return "", fmt.Errorf("搜狗触发反爬虫验证，链接被重定向到 antispider 页面")
		}
		// 如果是有效的微信链接，返回
		if strings.Contains(href, "mp.weixin.qq.com") {
			return href, nil
		}
	}

	return "", fmt.Errorf("无法解析重定向 URL")
}

// GetArticleContent 获取文章内容（带智能重试机制）
func (s *SogouScraper) GetArticleContent(ctx context.Context, link string) (string, string, string, error) {
	// 处理相对路径
	if strings.HasPrefix(link, "/") {
		link = "https://weixin.sogou.com" + link
	}
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		s.rateLimiter.Wait()

		if attempt > 0 {
			backoff := time.Duration(2*(attempt+1)) * time.Second
			logger.Log.Info("重试获取搜狗文章内容", zap.Int("attempt", attempt+1), zap.Int("max", maxRetries), zap.Duration("backoff", backoff))
			time.Sleep(backoff)
		}

		content, imageLinks, cleanContent, err := s.getArticleContentOnce(ctx, link)
		if err == nil && strings.TrimSpace(content) != "" {
			s.rateLimiter.RecordSuccess()
			return content, imageLinks, cleanContent, nil
		}

		s.rateLimiter.RecordFailure()
		lastErr = err
		logger.Log.Warn("获取搜狗文章内容失败", zap.Int("attempt", attempt+1), zap.Int("max", maxRetries), zap.Error(err))
	}

	return "", "", "", fmt.Errorf("获取文章内容失败，已重试 %d 次: %w", maxRetries, lastErr)
}

// getArticleContentOnce 单次获取文章内容
func (s *SogouScraper) getArticleContentOnce(ctx context.Context, link string) (string, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return "", "", "", err
	}

	// 设置更真实的请求头
	req.Header.Set("User-Agent", utils.GetRandomUserAgent())
	req.Header.Set("Referer", "https://weixin.sogou.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// 不显式设置 Accept-Encoding，让 Go 的 http.Client 自动处理 gzip 解压
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	logger.Log.Info("正在请求搜狗文章内容", zap.String("link", link))

	// 使用 contentClient（跟随重定向）获取文章内容
	resp, err := s.contentClient.Do(req)
	if err != nil {
		logger.Log.Error("请求搜狗文章失败", zap.Error(err))
		return "", "", "", err
	}
	defer resp.Body.Close()

	logger.Log.Debug("搜狗文章响应状态码", zap.Int("status", resp.StatusCode))

	// 检查最终 URL 是否指向 antispider 页面
	finalURL := resp.Request.URL.String()
	if strings.Contains(finalURL, "antispider") {
		return "", "", "", fmt.Errorf("搜狗触发反爬虫验证，请稍后重试")
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Log.Error("读取搜狗响应体失败", zap.Error(err))
		return "", "", "", err
	}

	logger.Log.Debug("搜狗响应体长度", zap.Int("bytes", len(bodyBytes)))

	bodyStr := string(bodyBytes)

	// 检测 antispider 页面内容
	if strings.Contains(bodyStr, "antispider") || strings.Contains(bodyStr, "请输入验证码") {
		return "", "", "", fmt.Errorf("搜狗触发反爬虫验证，请稍后重试")
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(bodyBytes)))
	if err != nil {
		logger.Log.Error("解析搜狗文章HTML失败", zap.Error(err))
		return "", "", "", err
	}

	// 尝试多个可能的内容选择器
	var contentHTML string
	var content *goquery.Selection

	content = doc.Find("#js_content")
	if content.Length() > 0 {
		contentHTML, err = content.Html()
		if err == nil && strings.TrimSpace(contentHTML) != "" {
			logger.Log.Debug("使用 #js_content 选择器成功", zap.Int("length", len(contentHTML)))
		}
	}

	if strings.TrimSpace(contentHTML) == "" {
		logger.Log.Debug("#js_content 为空，尝试 .rich_media_content")
		content = doc.Find(".rich_media_content")
		if content.Length() > 0 {
			contentHTML, err = content.Html()
			if err == nil && strings.TrimSpace(contentHTML) != "" {
				logger.Log.Debug("使用 .rich_media_content 选择器成功", zap.Int("length", len(contentHTML)))
			}
		}
	}

	if strings.TrimSpace(contentHTML) == "" {
		logger.Log.Debug(".rich_media_content 为空，尝试 article 标签")
		content = doc.Find("article")
		if content.Length() > 0 {
			contentHTML, err = content.Html()
			if err == nil && strings.TrimSpace(contentHTML) != "" {
				logger.Log.Debug("使用 article 选择器成功", zap.Int("length", len(contentHTML)))
			}
		}
	}

	if strings.TrimSpace(contentHTML) == "" {
		logger.Log.Error("搜狗文章所有内容选择器都失败", zap.String("html_preview", func() string {
			if len(bodyBytes) > 1000 {
				return string(bodyBytes[:1000])
			}
			return string(bodyBytes)
		}()))
		return "", "", "", fmt.Errorf("无法提取文章内容，所有选择器都返回空")
	}

	// 处理图片
	imageCount := 0
	imageURLSet := make(map[string]struct{})
	content.Find("img").Each(func(i int, img *goquery.Selection) {
		if dataSrc, exists := img.Attr("data-src"); exists && dataSrc != "" {
			cleanURL := strings.Split(dataSrc, "#")[0]
			img.SetAttr("src", cleanURL)
			imageURLSet[cleanURL] = struct{}{}
			imageCount++
			logger.Log.Debug("处理图片", zap.Int("index", imageCount), zap.String("url", cleanURL))
		} else if src, exists := img.Attr("src"); exists && strings.HasPrefix(src, "data:") {
			if originalSrc, exists := img.Attr("data-original-src"); exists && originalSrc != "" {
				cleanURL := strings.Split(originalSrc, "#")[0]
				img.SetAttr("src", cleanURL)
				imageURLSet[cleanURL] = struct{}{}
				imageCount++
				logger.Log.Debug("处理图片(data-original-src)", zap.Int("index", imageCount), zap.String("url", cleanURL))
			}
		} else if src, exists := img.Attr("src"); exists && strings.TrimSpace(src) != "" {
			cleanURL := strings.Split(src, "#")[0]
			imageURLSet[cleanURL] = struct{}{}
		}
	})

	logger.Log.Debug("搜狗图片处理完成", zap.Int("count", imageCount))

	// 重新获取处理后的 HTML
	contentHTML, err = content.Html()
	if err != nil {
		logger.Log.Error("获取处理后的HTML失败", zap.Error(err))
		return "", "", "", err
	}

	// 转换为 Markdown
	markdown, err := s.converter.ConvertString(contentHTML)
	if err != nil {
		logger.Log.Error("转换为Markdown失败", zap.Error(err))
		return "", "", "", err
	}

	imageURLs := make([]string, 0, len(imageURLSet))
	for imageURL := range imageURLSet {
		imageURLs = append(imageURLs, imageURL)
	}
	sort.Strings(imageURLs)
	imageLinks := strings.Join(imageURLs, "/n")

	cleanContent := cleanHTMLContent(contentHTML)

	logger.Log.Debug("搜狗文章成功转换为Markdown", zap.Int("length", len(markdown)))
	return markdown, imageLinks, cleanContent, nil
}

// FilterArticlesByDate 按日期过滤文章
func (s *SogouScraper) FilterArticlesByDate(articles []models.Article, startDate, endDate string) []models.Article {
	// 使用中国时区（UTC+8），与搜索结果中的时区保持一致
	chinaLoc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		chinaLoc = time.FixedZone("CST", 8*3600)
		logger.Log.Warn("无法加载 Asia/Shanghai 时区，使用固定 UTC+8", zap.Error(err))
	}
	start, _ := time.ParseInLocation("2006-01-02", startDate, chinaLoc)
	end, _ := time.ParseInLocation("2006-01-02", endDate, chinaLoc)
	end = end.Add(24 * time.Hour)

	filtered := make([]models.Article, 0)
	for _, article := range articles {
		publishTime := time.Unix(article.PublishTimestamp, 0).In(chinaLoc)
		if (publishTime.Equal(start) || publishTime.After(start)) && publishTime.Before(end) {
			filtered = append(filtered, article)
		} else {
			logger.Log.Debug("文章被日期过滤排除",
				zap.String("title", article.Title),
				zap.String("publishTime", publishTime.Format("2006-01-02 15:04:05")),
				zap.String("startDate", start.Format("2006-01-02 15:04:05")),
				zap.String("endDate", end.Format("2006-01-02 15:04:05")),
			)
		}
	}

	return filtered
}

// Cancel 取消爬取
func (s *SogouScraper) Cancel() {
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
}

// mergeCookies 合并 cookie 列表，后出现的覆盖先出现的
func mergeCookies(existing, newCookies []*http.Cookie) []*http.Cookie {
	cookieMap := make(map[string]*http.Cookie)
	for _, c := range existing {
		cookieMap[c.Name] = c
	}
	for _, c := range newCookies {
		cookieMap[c.Name] = c
	}
	result := make([]*http.Cookie, 0, len(cookieMap))
	for _, c := range cookieMap {
		result = append(result, c)
	}
	return result
}
