import {
	addTokenUsage,
	emptyTokenUsage,
	type LocalUsageEvent,
	type TokenUsage,
} from '../local/types.ts'
import type { ProviderId } from '../schema/model.ts'

export type CostState = {
	costUSD: number | null
}

export type ModelAggregate = TokenUsage &
	CostState & {
		model: string
		pricingState: 'known' | 'missing' | 'embedded' | 'zero'
	}

export type AggregateBucket = TokenUsage &
	CostState & {
		providerId: ProviderId
		models: Map<string, ModelAggregate>
		events: number
	}

export function createBucket(providerId: ProviderId): AggregateBucket {
	return {
		...emptyTokenUsage(),
		providerId,
		costUSD: null,
		models: new Map(),
		events: 0,
	}
}

export function addEventToBucket(bucket: AggregateBucket, event: LocalUsageEvent): void {
	addTokenUsage(bucket, event)
	bucket.events += 1
	if (event.costUSD != null) {
		bucket.costUSD = (bucket.costUSD ?? 0) + event.costUSD
	}

	const model = bucket.models.get(event.model) ?? {
		...emptyTokenUsage(),
		model: event.model,
		costUSD: null,
		pricingState: 'missing' as const,
	}
	addTokenUsage(model, event)
	if (event.costUSD != null) {
		model.costUSD = (model.costUSD ?? 0) + event.costUSD
		model.pricingState = 'embedded'
	}
	bucket.models.set(event.model, model)
}

export function serializeModels(bucket: AggregateBucket): ModelAggregate[] {
	return Array.from(bucket.models.values()).sort((a, b) => {
		const costDiff = (b.costUSD ?? 0) - (a.costUSD ?? 0)
		if (costDiff !== 0) {
			return costDiff
		}
		return b.totalTokens - a.totalTokens
	})
}

export function dateKey(timestamp: string, timezone?: string): string {
	const date = new Date(timestamp)
	if (Number.isNaN(date.getTime())) {
		return timestamp.slice(0, 10)
	}
	if (timezone == null) {
		return date.toISOString().slice(0, 10)
	}
	return new Intl.DateTimeFormat('en-CA', {
		timeZone: timezone,
		year: 'numeric',
		month: '2-digit',
		day: '2-digit',
	}).format(date)
}

export function monthKey(timestamp: string, timezone?: string): string {
	return dateKey(timestamp, timezone).slice(0, 7)
}

export function weekKey(timestamp: string, timezone?: string): string {
	const date = new Date(`${dateKey(timestamp, timezone)}T00:00:00.000Z`)
	const day = date.getUTCDay()
	const diff = day === 0 ? -6 : 1 - day
	date.setUTCDate(date.getUTCDate() + diff)
	return date.toISOString().slice(0, 10)
}
