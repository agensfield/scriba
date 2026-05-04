import { z } from 'zod'

export const SCHEMA_VERSION = 'scriba.alpha.v1'

export const providerIdSchema = z.union([
	z.literal('claude'),
	z.literal('codex'),
	z.string().min(1),
])

export const sourceKindSchema = z.enum(['local-log', 'provider-api', 'cache'])

export const sourceProvenanceSchema = z.object({
	kind: sourceKindSchema,
	providerId: providerIdSchema,
	fetchedAt: z.iso.datetime().optional(),
	cacheAgeMs: z.number().int().nonnegative().optional(),
	stale: z.boolean().optional(),
	error: z.string().optional(),
})

export const metricFormatSchema = z.discriminatedUnion('kind', [
	z.object({ kind: z.literal('percent') }),
	z.object({ kind: z.literal('dollars') }),
	z.object({ kind: z.literal('count'), suffix: z.string() }),
])

const metricBaseSchema = z.object({
	label: z.string().min(1),
	provenance: z.array(sourceProvenanceSchema).optional(),
})

export const metricLineSchema = z.discriminatedUnion('type', [
	metricBaseSchema.extend({
		type: z.literal('text'),
		value: z.string(),
	}),
	metricBaseSchema.extend({
		type: z.literal('progress'),
		used: z.number().finite(),
		limit: z.number().finite().positive(),
		format: metricFormatSchema,
		resetsAt: z.iso.datetime().optional(),
		periodDurationMs: z.number().int().positive().optional(),
	}),
	metricBaseSchema.extend({
		type: z.literal('badge'),
		text: z.string(),
	}),
])

export const providerSnapshotSchema = z.object({
	providerId: providerIdSchema,
	displayName: z.string().min(1),
	plan: z.string().optional(),
	lines: z.array(metricLineSchema),
	provenance: z.array(sourceProvenanceSchema),
})

export const statusSnapshotSchema = z.object({
	schemaVersion: z.literal(SCHEMA_VERSION),
	generatedAt: z.iso.datetime(),
	providers: z.array(providerSnapshotSchema),
})

export const tokenUsageSchema = z.object({
	inputTokens: z.number().int().nonnegative(),
	outputTokens: z.number().int().nonnegative(),
	cacheCreationTokens: z.number().int().nonnegative().default(0),
	cacheReadTokens: z.number().int().nonnegative().default(0),
	cachedInputTokens: z.number().int().nonnegative().default(0),
	reasoningOutputTokens: z.number().int().nonnegative().default(0),
	totalTokens: z.number().int().nonnegative(),
})

export const modelBreakdownSchema = tokenUsageSchema.extend({
	model: z.string().min(1),
	costUSD: z.number().nonnegative().nullable(),
	pricingState: z.enum(['known', 'missing', 'embedded', 'zero']).default('missing'),
})

export const reportTotalsSchema = tokenUsageSchema.extend({
	costUSD: z.number().nonnegative().nullable(),
})

export const dailyReportRowSchema = reportTotalsSchema.extend({
	date: z.string().regex(/^\d{4}-\d{2}-\d{2}$/),
	providerId: providerIdSchema,
	models: z.array(modelBreakdownSchema),
	project: z.string().optional(),
})

export const monthlyReportRowSchema = reportTotalsSchema.extend({
	month: z.string().regex(/^\d{4}-\d{2}$/),
	providerId: providerIdSchema,
	models: z.array(modelBreakdownSchema),
	project: z.string().optional(),
})

export const weeklyReportRowSchema = reportTotalsSchema.extend({
	week: z.string().regex(/^\d{4}-\d{2}-\d{2}$/),
	providerId: providerIdSchema,
	models: z.array(modelBreakdownSchema),
	project: z.string().optional(),
})

export const sessionReportRowSchema = reportTotalsSchema.extend({
	sessionId: z.string().min(1),
	providerId: providerIdSchema,
	lastActivity: z.iso.datetime().or(z.string().regex(/^\d{4}-\d{2}-\d{2}$/)),
	projectPath: z.string().optional(),
	directory: z.string().optional(),
	sessionFile: z.string().optional(),
	models: z.array(modelBreakdownSchema),
})

export const blockReportRowSchema = reportTotalsSchema.extend({
	id: z.string().min(1),
	providerId: z.literal('claude'),
	startTime: z.iso.datetime(),
	endTime: z.iso.datetime(),
	actualEndTime: z.iso.datetime().optional(),
	isActive: z.boolean(),
	isGap: z.boolean(),
	entries: z.number().int().nonnegative(),
	models: z.array(modelBreakdownSchema),
	usageLimitResetTime: z.iso.datetime().optional(),
})

export const okEnvelopeSchema = z.object({
	ok: z.literal(true),
	schemaVersion: z.literal(SCHEMA_VERSION),
	data: z.unknown(),
	meta: z
		.object({
			generatedAt: z.iso.datetime(),
			command: z.array(z.string()).optional(),
			warnings: z.array(z.string()).optional(),
		})
		.optional(),
})

export const errorEnvelopeSchema = z.object({
	ok: z.literal(false),
	schemaVersion: z.literal(SCHEMA_VERSION),
	error: z.object({
		code: z.string(),
		message: z.string(),
		details: z.unknown().optional(),
	}),
})

export const schemaRegistry = {
	statusSnapshot: statusSnapshotSchema,
	providerSnapshot: providerSnapshotSchema,
	metricLine: metricLineSchema,
	sourceProvenance: sourceProvenanceSchema,
	dailyReportRow: dailyReportRowSchema,
	weeklyReportRow: weeklyReportRowSchema,
	monthlyReportRow: monthlyReportRowSchema,
	sessionReportRow: sessionReportRowSchema,
	blockReportRow: blockReportRowSchema,
	okEnvelope: okEnvelopeSchema,
	errorEnvelope: errorEnvelopeSchema,
}

export type ProviderId = z.infer<typeof providerIdSchema>
export type SourceKind = z.infer<typeof sourceKindSchema>
export type SourceProvenance = z.infer<typeof sourceProvenanceSchema>
export type MetricFormat = z.infer<typeof metricFormatSchema>
export type MetricLine = z.infer<typeof metricLineSchema>
export type ProviderSnapshot = z.infer<typeof providerSnapshotSchema>
export type StatusSnapshot = z.infer<typeof statusSnapshotSchema>
