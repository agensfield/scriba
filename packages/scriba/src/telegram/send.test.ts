import { describe, expect, it } from 'vitest'
import { sendTelegramAlerts } from './send.ts'

describe('sendTelegramAlerts', () => {
	it('sends alert messages through Telegram API', async () => {
		const requests: unknown[] = []
		const sent = await sendTelegramAlerts({
			botToken: 'token',
			chatId: 'chat',
			alerts: [
				{ providerId: 'codex', label: 'Session', severity: 'warning', message: 'Codex high' },
			],
			fetch: async (_url, init) => {
				requests.push(JSON.parse(String(init?.body)))
				return Response.json({ ok: true })
			},
		})
		expect(sent).toBe(1)
		expect(requests).toEqual([{ chat_id: 'chat', text: '[Scriba] WARNING: Codex high' }])
	})
})
