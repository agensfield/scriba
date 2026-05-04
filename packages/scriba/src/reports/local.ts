import type { LocalUsageEvent } from '../local/types.ts'
import {
	addEventToBucket,
	createBucket,
	dateKey,
	monthKey,
	serializeModels,
	weekKey,
} from './aggregate.ts'

export type ReportOptions = {
	timezone?: string
	order?: 'asc' | 'desc'
}

function sortRows<T>(rows: T[], getKey: (row: T) => string, order: 'asc' | 'desc' = 'asc'): T[] {
	return rows.sort((a, b) => {
		const result = getKey(a).localeCompare(getKey(b))
		return order === 'asc' ? result : -result
	})
}

export function buildDailyReport(events: LocalUsageEvent[], options: ReportOptions = {}) {
	const buckets = new Map<string, ReturnType<typeof createBucket>>()
	for (const event of events) {
		const key = dateKey(event.timestamp, options.timezone)
		const bucket = buckets.get(key) ?? createBucket(event.providerId)
		addEventToBucket(bucket, event)
		buckets.set(key, bucket)
	}

	return sortRows(
		Array.from(buckets.entries()).map(([date, bucket]) => ({
			date,
			providerId: bucket.providerId,
			inputTokens: bucket.inputTokens,
			outputTokens: bucket.outputTokens,
			cacheCreationTokens: bucket.cacheCreationTokens,
			cacheReadTokens: bucket.cacheReadTokens,
			cachedInputTokens: bucket.cachedInputTokens,
			reasoningOutputTokens: bucket.reasoningOutputTokens,
			totalTokens: bucket.totalTokens,
			costUSD: bucket.costUSD,
			models: serializeModels(bucket),
		})),
		(row) => row.date,
		options.order,
	)
}

export function buildMonthlyReport(events: LocalUsageEvent[], options: ReportOptions = {}) {
	const buckets = new Map<string, ReturnType<typeof createBucket>>()
	for (const event of events) {
		const key = monthKey(event.timestamp, options.timezone)
		const bucket = buckets.get(key) ?? createBucket(event.providerId)
		addEventToBucket(bucket, event)
		buckets.set(key, bucket)
	}

	return sortRows(
		Array.from(buckets.entries()).map(([month, bucket]) => ({
			month,
			providerId: bucket.providerId,
			inputTokens: bucket.inputTokens,
			outputTokens: bucket.outputTokens,
			cacheCreationTokens: bucket.cacheCreationTokens,
			cacheReadTokens: bucket.cacheReadTokens,
			cachedInputTokens: bucket.cachedInputTokens,
			reasoningOutputTokens: bucket.reasoningOutputTokens,
			totalTokens: bucket.totalTokens,
			costUSD: bucket.costUSD,
			models: serializeModels(bucket),
		})),
		(row) => row.month,
		options.order,
	)
}

export function buildWeeklyReport(events: LocalUsageEvent[], options: ReportOptions = {}) {
	const buckets = new Map<string, ReturnType<typeof createBucket>>()
	for (const event of events) {
		const key = weekKey(event.timestamp, options.timezone)
		const bucket = buckets.get(key) ?? createBucket(event.providerId)
		addEventToBucket(bucket, event)
		buckets.set(key, bucket)
	}

	return sortRows(
		Array.from(buckets.entries()).map(([week, bucket]) => ({
			week,
			providerId: bucket.providerId,
			inputTokens: bucket.inputTokens,
			outputTokens: bucket.outputTokens,
			cacheCreationTokens: bucket.cacheCreationTokens,
			cacheReadTokens: bucket.cacheReadTokens,
			cachedInputTokens: bucket.cachedInputTokens,
			reasoningOutputTokens: bucket.reasoningOutputTokens,
			totalTokens: bucket.totalTokens,
			costUSD: bucket.costUSD,
			models: serializeModels(bucket),
		})),
		(row) => row.week,
		options.order,
	)
}

export function buildSessionReport(events: LocalUsageEvent[], options: ReportOptions = {}) {
	const buckets = new Map<
		string,
		ReturnType<typeof createBucket> & {
			sessionId: string
			lastActivity: string
			projectPath?: string | undefined
			directory?: string | undefined
			sessionFile?: string | undefined
		}
	>()
	for (const event of events) {
		const key = event.sessionId
		const bucket = buckets.get(key) ?? {
			...createBucket(event.providerId),
			sessionId: event.sessionId,
			lastActivity: event.timestamp,
			projectPath: event.projectPath,
			directory: event.directory,
			sessionFile: event.sessionFile,
		}
		addEventToBucket(bucket, event)
		if (event.timestamp > bucket.lastActivity) {
			bucket.lastActivity = event.timestamp
		}
		buckets.set(key, bucket)
	}

	return sortRows(
		Array.from(buckets.values()).map((bucket) => ({
			sessionId: bucket.sessionId,
			providerId: bucket.providerId,
			lastActivity: bucket.lastActivity,
			projectPath: bucket.projectPath,
			directory: bucket.directory,
			sessionFile: bucket.sessionFile,
			inputTokens: bucket.inputTokens,
			outputTokens: bucket.outputTokens,
			cacheCreationTokens: bucket.cacheCreationTokens,
			cacheReadTokens: bucket.cacheReadTokens,
			cachedInputTokens: bucket.cachedInputTokens,
			reasoningOutputTokens: bucket.reasoningOutputTokens,
			totalTokens: bucket.totalTokens,
			costUSD: bucket.costUSD,
			models: serializeModels(bucket),
		})),
		(row) => row.lastActivity,
		options.order ?? 'desc',
	)
}
