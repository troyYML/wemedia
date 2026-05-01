export interface Config {
  maxPages: number
  requestInterval: number
  requestIntervalMin: number
  requestIntervalMax: number
  maxWorkers: number
  includeContent: boolean
  cacheExpireHours: number
  outputDir: string
}

export interface ScrapeConfig {
  accounts: string[]
  startDate: string
  endDate: string
  recentDays: number
  maxPages: number
  requestInterval: number
  requestIntervalMin: number
  requestIntervalMax: number
  includeContent: boolean
  keywordFilter: string
  maxWorkers: number
}

export interface SogouSearchConfig {
  keywords: string[]
  maxPages: number
  requestIntervalMin: number
  requestIntervalMax: number
  includeContent: boolean
  startDate: string
  endDate: string
}
