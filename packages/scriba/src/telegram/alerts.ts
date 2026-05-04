import type { ScribaConfig } from '../config/schema.ts'
import type { MetricLine, ProviderSnapshot, StatusSnapshot } from '../schema/model.ts'

export type TelegramAlert = {
	providerId: string
	label: string
	severity: 'warning' | 'error'
	message: string
}

export function evaluateTelegramAlerts(
	snapshot: StatusSnapshot,
	config: ScribaConfig['telegram'],
): TelegramAlert[] {
	if (!config.enabled) {
		return []
	}

	const alerts: TelegramAlert[] = []
	for (const provider of snapshot.providers) {
		for (const line of provider.lines) {
			const alert = alertForLine(provider, line, config)
			if (alert != null) {
				alerts.push(alert)
			}
		}
		for (const provenance of provider.provenance) {
			if (config.alerts.includeErrors && provenance.error != null) {
				alerts.push({
					providerId: provider.providerId,
					label: 'Provider error',
					severity: 'error',
					message: `${provider.displayName}: ${provenance.error}`,
				})
			}
		}
	}
	return alerts
}

function alertForLine(
	provider: ProviderSnapshot,
	line: MetricLine,
	config: ScribaConfig['telegram'],
): TelegramAlert | null {
	if (line.type !== 'progress' || line.format.kind !== 'percent') {
		return null
	}
	const threshold = line.label.toLowerCase().includes('weekly')
		? config.alerts.weeklyPercent
		: config.alerts.sessionPercent
	const percent = (line.used / line.limit) * 100
	if (percent < threshold) {
		return null
	}
	return {
		providerId: provider.providerId,
		label: line.label,
		severity: 'warning',
		message: `${provider.displayName} ${line.label} usage is at ${Math.round(percent)}%.`,
	}
}

export function renderTelegramAlert(alert: TelegramAlert): string {
	return `[Scriba] ${alert.severity.toUpperCase()}: ${alert.message}`
}
