package telegram

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/server/store"
	"github.com/go-telegram/bot/models"
)

var telegramAccountSelectorPattern = regexp.MustCompile(`^(?:acct-[0-9a-f]{20}|[a-z0-9]+(?:-[a-z0-9]+)*)$`)
var telegramAccountIDPattern = regexp.MustCompile(`^acct-[0-9a-f]{20}$`)

func commandAccount(fields []string) (string, error) {
	if len(fields) == 1 {
		return "", nil
	}
	if len(fields) != 2 || len(fields[1]) > 32 || !telegramAccountSelectorPattern.MatchString(fields[1]) {
		return "", accounts.ErrAccountNotFound
	}
	return fields[1], nil
}

func renderSelectedAccount(account store.Account, stale bool, body string) string {
	header := "<b>Account</b> " + html.EscapeString(account.DisplayName()) + "\n<code>" + html.EscapeString(account.ID) + "</code>"
	if stale {
		header += "\n<i>stored observation is stale</i>"
	}
	if !account.CredentialsAvailable {
		header += "\n<i>credentials unavailable; showing stored data</i>"
	}
	return header + "\n\n" + body
}

func parseAccountCallback(data string) (string, string, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != "accounts" || parts[1] != "v1" {
		return "", "", false
	}
	action, value := parts[2], parts[3]
	if action == "list" {
		if len(value) == 0 || len(value) > 4 {
			return "", "", false
		}
		for _, char := range value {
			if char < '0' || char > '9' {
				return "", "", false
			}
		}
		return action, value, true
	}
	if action != "open" && action != "limits" && action != "grants" && action != "reset" && action != "activity" {
		return "", "", false
	}
	if !telegramAccountIDPattern.MatchString(value) {
		return "", "", false
	}
	return action, value, true
}

func accountByID(accounts []store.Account, id string) (store.Account, bool) {
	for _, account := range accounts {
		if account.ID == id {
			return account, true
		}
	}
	return store.Account{}, false
}

func renderAccountLanding(account store.Account) string {
	state := "credentials unavailable"
	if account.CredentialsAvailable {
		state = "credentials available"
	}
	return "<b>Codex account</b>\n\n<b>" + html.EscapeString(account.DisplayName()) + "</b>\n<code>" + html.EscapeString(account.ID) + "</code>\n" + state + "\n" + renderAccountFreshness(account) + "\n\nChoose a view."
}

func accountsKeyboard(accounts []store.Account, page int) models.InlineKeyboardMarkup {
	pages := max(1, (len(accounts)+accountsPageSize-1)/accountsPageSize)
	start := min(max(page, 0)*accountsPageSize, len(accounts))
	end := min(start+accountsPageSize, len(accounts))
	rows := make([][]models.InlineKeyboardButton, 0, accountsPageSize+2)
	for _, account := range accounts[start:end] {
		rows = append(rows, []models.InlineKeyboardButton{{Text: accountButtonLabel(account), CallbackData: "accounts:v1:open:" + account.ID}})
	}
	if pages > 1 {
		var nav []models.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, models.InlineKeyboardButton{Text: "‹ Prev", CallbackData: "accounts:v1:list:" + strconv.Itoa(page-1)})
		}
		if page+1 < pages {
			nav = append(nav, models.InlineKeyboardButton{Text: "Next ›", CallbackData: "accounts:v1:list:" + strconv.Itoa(page+1)})
		}
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Main menu", CallbackData: "quick:home"}})
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func accountKeyboard(accountID string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Limits", CallbackData: "accounts:v1:limits:" + accountID}, {Text: "Grants", CallbackData: "accounts:v1:grants:" + accountID}},
		{{Text: "Reset limits", CallbackData: "accounts:v1:reset:" + accountID}, {Text: "Activity", CallbackData: "accounts:v1:activity:" + accountID}},
		{{Text: "‹ All accounts", CallbackData: "accounts:v1:list:0"}},
	}}
}

func accountsBackKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "‹ All accounts", CallbackData: "accounts:v1:list:0"}}}}
}

func truncateButtonLabel(label string) string {
	runes := []rune(label)
	if len(runes) <= 64 {
		return label
	}
	return string(runes[:63]) + "…"
}

func accountButtonLabel(account store.Account) string {
	suffix := " · offline"
	if account.CredentialsAvailable {
		suffix = " · ready"
	}
	budget := 64 - len([]rune(suffix))
	label := []rune(account.DisplayName())
	if budget < 1 {
		return truncateButtonLabel(strings.TrimSpace(suffix))
	}
	if len(label) > budget {
		if budget == 1 {
			label = []rune("…")
		} else {
			label = append(label[:budget-1], '…')
		}
	}
	return string(label) + suffix
}
