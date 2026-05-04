import type { LocalUsageEvent } from '../local/types.ts'
import { addEventToBucket, createBucket, serializeModels } from './aggregate.ts'

export type BlockReportOptions = {
	now?: Date
	sessionDurationHours?: number
}

export function buildClaudeBlocks(events: LocalUsageEvent[], options: BlockReportOptions = {}) {
	const durationMs = (options.sessionDurationHours ?? 5) * 60 * 60 * 1000
	const sorted = events
		.filter((event) => event.providerId === 'claude')
		.toSorted((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
	const rows: ReturnType<typeof blockRow>[] = []
	let current: ReturnType<typeof createBlock> | null = null

	for (const event of sorted) {
		const eventTime = new Date(event.timestamp).getTime()
		if (
			current == null ||
			eventTime - current.startMs >= durationMs ||
			eventTime - current.lastEventMs >= durationMs
		) {
			if (current != null) {
				rows.push(blockRow(current, durationMs, options.now))
			}
			current = createBlock(event, durationMs)
		}
		current.lastEventMs = eventTime
		current.actualEndTime = event.timestamp
		addEventToBucket(current.bucket, event)
	}

	if (current != null) {
		rows.push(blockRow(current, durationMs, options.now))
	}

	return rows.toReversed()
}

function createBlock(event: LocalUsageEvent, durationMs: number) {
	const start = new Date(event.timestamp)
	start.setUTCMinutes(0, 0, 0)
	const startMs = start.getTime()
	return {
		id: start.toISOString(),
		startMs,
		lastEventMs: new Date(event.timestamp).getTime(),
		startTime: start.toISOString(),
		endTime: new Date(startMs + durationMs).toISOString(),
		actualEndTime: event.timestamp,
		bucket: createBucket('claude'),
	}
}

function blockRow(block: ReturnType<typeof createBlock>, durationMs: number, now = new Date()) {
	const nowMs = now.getTime()
	const startMs = new Date(block.startTime).getTime()
	const endMs = startMs + durationMs
	return {
		id: block.id,
		providerId: 'claude' as const,
		startTime: block.startTime,
		endTime: block.endTime,
		actualEndTime: block.actualEndTime,
		isActive: nowMs >= startMs && nowMs < endMs && nowMs - block.lastEventMs < durationMs,
		isGap: false,
		entries: block.bucket.events,
		inputTokens: block.bucket.inputTokens,
		outputTokens: block.bucket.outputTokens,
		cacheCreationTokens: block.bucket.cacheCreationTokens,
		cacheReadTokens: block.bucket.cacheReadTokens,
		cachedInputTokens: block.bucket.cachedInputTokens,
		reasoningOutputTokens: block.bucket.reasoningOutputTokens,
		totalTokens: block.bucket.totalTokens,
		costUSD: block.bucket.costUSD,
		models: serializeModels(block.bucket),
	}
}
