import { describe, expect, it } from 'vitest'
import { scribaConfigSchema } from '../config/schema.ts'
import { SCHEMA_VERSION, type StatusSnapshot } from '../schema/model.ts'
import { evaluateTelegramAlerts, renderTelegramAlert } from './alerts.ts'

describe('telegram alerts', () => {
	it('emits threshold alerts from progress lines', () => {
		const config = scribaConfigSchema.parse({
			telegram: { enabled: true, alerts: { sessionPercent: 80 } },
		}).telegram
		const snapshot: StatusSnapshot = {
			schemaVersion: SCHEMA_VERSION,
			generatedAt: '2026-05-05T00:00:00.000Z',
			providers: [
				{
					providerId: 'codex',
					displayName: 'Codex',
					provenance: [],
					lines: [
						{
							type: 'progress',
							label: 'Session',
							used: 90,
							limit: 100,
							format: { kind: 'percent' },
						},
					],
				},
			],
		}

		const alerts = evaluateTelegramAlerts(snapshot, config)
		expect(alerts).toHaveLength(1)
		const [alert] = alerts
		expect(alert).toBeDefined()
		if (alert != null) {
			expect(renderTelegramAlert(alert)).toContain('Codex Session')
		}
	})
})
