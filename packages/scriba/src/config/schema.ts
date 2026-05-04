import { z } from 'zod'

const pathListSchema = z.array(z.string().min(1)).default([])

export const telegramConfigSchema = z.object({
	enabled: z.boolean().default(false),
	botTokenEnv: z.string().default('SCRIBA_TELEGRAM_BOT_TOKEN'),
	chatId: z.string().optional(),
	alerts: z
		.object({
			sessionPercent: z.number().min(0).max(100).default(80),
			weeklyPercent: z.number().min(0).max(100).default(80),
			creditsBelowUSD: z.number().nonnegative().optional(),
			includeErrors: z.boolean().default(true),
		})
		.default({
			sessionPercent: 80,
			weeklyPercent: 80,
			includeErrors: true,
		}),
})

export const providerConfigSchema = z.object({
	enabled: z.boolean().default(true),
	paths: pathListSchema,
})

export const scribaConfigSchema = z.object({
	schemaVersion: z.literal(1).default(1),
	cacheDir: z.string().optional(),
	timezone: z.string().optional(),
	locale: z.string().default('en-US'),
	output: z
		.object({
			json: z.boolean().default(false),
			color: z.boolean().optional(),
		})
		.default({ json: false }),
	providers: z
		.object({
			claude: providerConfigSchema.default({ enabled: true, paths: [] }),
			codex: providerConfigSchema.default({ enabled: true, paths: [] }),
		})
		.default({
			claude: { enabled: true, paths: [] },
			codex: { enabled: true, paths: [] },
		}),
	telegram: telegramConfigSchema.default({
		enabled: false,
		botTokenEnv: 'SCRIBA_TELEGRAM_BOT_TOKEN',
		alerts: {
			sessionPercent: 80,
			weeklyPercent: 80,
			includeErrors: true,
		},
	}),
})

export type ScribaConfig = z.infer<typeof scribaConfigSchema>
