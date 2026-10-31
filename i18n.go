package main

import (
	"net/http"
	"strings"
)

// Lang holds all user-facing texts of one language.
type Lang struct {
	Code       string
	DateFormat string // Go time layout for "link valid until"

	Title        string
	Hello        string // %s = guest name
	Welcome      string
	Hint         string
	ValidUntil   string // %s = date
	ExpiredTitle string
	Expired      string
	ExpiredNamed string // %s = guest name
	InvalidTitle string
	Invalid      string
	NoScript     string

	Forbidden     string
	LinkInvalid   string
	LinkExpired   string
	AlreadyOpen   string
	OpeningMsg    string
	WebhookFailed string

	JS JSTexts // passed to app.js
}

// JSTexts are used by app.js; "{s}" is replaced with a number of seconds.
type JSTexts struct {
	Open           string `json:"open"`
	Opening        string `json:"opening"`
	OpeningSub     string `json:"openingSub"`
	OpeningStatus  string `json:"openingStatus"`
	IsOpen         string `json:"isOpen"`
	IsOpenSub      string `json:"isOpenSub"`
	IsOpenStatus   string `json:"isOpenStatus"`
	Closing        string `json:"closing"`
	ClosingSub     string `json:"closingSub"`
	ClosingStatus  string `json:"closingStatus"`
	Closed         string `json:"closed"`
	Busy           string `json:"busy"`
	BusyStatus     string `json:"busyStatus"`
	Retry          string `json:"retry"`
	Unavailable    string `json:"unavailable"`
	Offline        string `json:"offline"`
	Inactive       string `json:"inactive"`
	GenericFailure string `json:"genericFailure"`
}

var langs = map[string]*Lang{
	"en": {
		Code:       "en",
		DateFormat: "Jan 2, 2006, 15:04",

		Title:        "Gate",
		Hello:        "Hi, %s!",
		Welcome:      "Welcome!",
		Hint:         "Drive up to the gate and press the button.",
		ValidUntil:   "Link valid until %s",
		ExpiredTitle: "Link expired",
		Expired:      "This link is no longer active. Ask your host for a new one.",
		ExpiredNamed: "%s, this link is no longer active. Ask your host for a new one.",
		InvalidTitle: "Invalid link",
		Invalid:      "Make sure you copied the whole address, or ask your host for a new link.",
		NoScript:     "This page needs JavaScript.",

		Forbidden:     "Request not allowed.",
		LinkInvalid:   "This link is invalid.",
		LinkExpired:   "This link has expired. Ask your host for a new one.",
		AlreadyOpen:   "The gate is already open.",
		OpeningMsg:    "The gate is opening!",
		WebhookFailed: "Could not reach the gate. Please try again.",

		JS: JSTexts{
			Open:           "Open the gate",
			Opening:        "Opening…",
			OpeningSub:     "{s} left",
			OpeningStatus:  "The gate is opening. Please wait a moment.",
			IsOpen:         "Open!",
			IsOpenSub:      "closes in {s}",
			IsOpenStatus:   "Come on in. Welcome!",
			Closing:        "Closing",
			ClosingSub:     "tap to open",
			ClosingStatus:  "The gate is closing. Tap the button to open it again.",
			Closed:         "The gate is closed.",
			Busy:           "Connecting…",
			BusyStatus:     "Connecting to the gate…",
			Retry:          "Try again",
			Unavailable:    "Unavailable",
			Offline:        "No internet connection. Please try again.",
			Inactive:       "This link is no longer active.",
			GenericFailure: "Something went wrong. Please try again.",
		},
	},
	"pl": {
		Code:       "pl",
		DateFormat: "02.01.2006, 15:04",

		Title:        "Brama",
		Hello:        "Cześć, %s!",
		Welcome:      "Witaj!",
		Hint:         "Podjedź pod bramę i naciśnij przycisk.",
		ValidUntil:   "Link ważny do %s",
		ExpiredTitle: "Link wygasł",
		Expired:      "Ten link nie jest już aktywny. Poproś gospodarza o nowy.",
		ExpiredNamed: "%s, ten link nie jest już aktywny. Poproś gospodarza o nowy.",
		InvalidTitle: "Nieprawidłowy link",
		Invalid:      "Sprawdź, czy skopiowałeś cały adres, albo poproś gospodarza o nowy link.",
		NoScript:     "Ta strona wymaga JavaScriptu.",

		Forbidden:     "Niedozwolone żądanie.",
		LinkInvalid:   "Link jest nieprawidłowy.",
		LinkExpired:   "Link wygasł. Poproś gospodarza o nowy.",
		AlreadyOpen:   "Brama jest już otwarta.",
		OpeningMsg:    "Brama się otwiera!",
		WebhookFailed: "Nie udało się połączyć z bramą. Spróbuj ponownie.",

		JS: JSTexts{
			Open:           "Otwórz bramę",
			Opening:        "Otwiera się…",
			OpeningSub:     "jeszcze {s}",
			OpeningStatus:  "Brama się otwiera. Poczekaj chwilę.",
			IsOpen:         "Otwarta!",
			IsOpenSub:      "zamknie się za {s}",
			IsOpenStatus:   "Wjeżdżaj śmiało. Zapraszamy!",
			Closing:        "Zamyka się",
			ClosingSub:     "dotknij, aby otworzyć",
			ClosingStatus:  "Brama się zamyka. Dotknij przycisku, aby otworzyć ją ponownie.",
			Closed:         "Brama zamknięta.",
			Busy:           "Łączę…",
			BusyStatus:     "Łączę się z bramą…",
			Retry:          "Spróbuj ponownie",
			Unavailable:    "Niedostępne",
			Offline:        "Brak połączenia z internetem. Spróbuj ponownie.",
			Inactive:       "Link jest nieaktywny.",
			GenericFailure: "Coś poszło nie tak. Spróbuj ponownie.",
		},
	},
}

// pickLang returns the first supported language from Accept-Language
// (browsers list them in order of preference), or def.
func pickLang(r *http.Request, def *Lang) *Lang {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		base, _, _ := strings.Cut(tag, "-")
		if l, ok := langs[strings.ToLower(base)]; ok {
			return l
		}
	}
	return def
}
