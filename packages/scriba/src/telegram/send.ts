import type { FetchLike } from '../remote/types.ts'
import { renderTelegramAlert, type TelegramAlert } from './alerts.ts'

export type SendTelegramAlertsOptions = {
	botToken: string
	chatId: string
	alerts: TelegramAlert[]
	fetch?: FetchLike | undefined
}

export async function sendTelegramAlerts(options: SendTelegramAlertsOptions): Promise<number> {
	const fetchImpl = options.fetch ?? fetch
	let sent = 0
	for (const alert of options.alerts) {
		const resp = await fetchImpl(
			`https://api.telegram.org/bot${encodeURIComponent(options.botToken)}/sendMessage`,
			{
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					chat_id: options.chatId,
					text: renderTelegramAlert(alert),
				}),
			},
		)
		if (!resp.ok) {
			throw new Error(`Telegram send failed: ${resp.status}`)
		}
		sent += 1
	}
	return sent
}
