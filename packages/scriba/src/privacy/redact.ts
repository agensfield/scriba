const EMAIL_RE = /[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi

export function redactForSharing<T>(value: T): T {
	return redactValue(value, undefined) as T
}

function redactValue(value: unknown, key: string | undefined): unknown {
	if (typeof value === 'string') {
		return redactString(value, key)
	}
	if (Array.isArray(value)) {
		return value.map((entry) => redactValue(entry, key))
	}
	if (value != null && typeof value === 'object') {
		return Object.fromEntries(
			Object.entries(value).map(([entryKey, entryValue]) => [
				entryKey,
				redactValue(entryValue, entryKey),
			]),
		)
	}
	return value
}

function redactString(value: string, key: string | undefined): string {
	const emailRedacted = value.replace(EMAIL_RE, '[redacted-email]')
	const lowerKey = key?.toLowerCase() ?? ''
	if (
		lowerKey.includes('path') ||
		lowerKey.includes('dir') ||
		lowerKey.includes('file') ||
		lowerKey.includes('source') ||
		emailRedacted.startsWith('/') ||
		emailRedacted.startsWith('~')
	) {
		return '[redacted-path]'
	}
	if (lowerKey.includes('account') || lowerKey.includes('token') || lowerKey.includes('auth')) {
		return '[redacted]'
	}
	return emailRedacted
}
