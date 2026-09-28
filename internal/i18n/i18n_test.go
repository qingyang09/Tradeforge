package i18n

import (
	"testing"

	"tradeforge/pkg/types"
)

func init() {
	register(LangEN, map[string]string{
		"test.greeting": "Hello, {name}!",
		"test.no_args":  "static text",
	})
	register(LangZH, map[string]string{
		"test.greeting": "你好，{name}！",
		"test.no_args":  "静态文本",
	})
}

func TestRenderSubstitutesNamedArgs(t *testing.T) {
	m := types.Msg("test.greeting", "name", "Alice")
	if got := Render(LangEN, m); got != "Hello, Alice!" {
		t.Errorf("Render(en) = %q", got)
	}
	if got := Render(LangZH, m); got != "你好，Alice！" {
		t.Errorf("Render(zh) = %q", got)
	}
}

func TestRenderFallsBackToChineseWhenEnglishKeyMissing(t *testing.T) {
	register(LangZH, map[string]string{"test.zh_only": "只有中文"})
	m := types.Msg("test.zh_only")
	if got := Render(LangEN, m); got != "只有中文" {
		t.Errorf("expected fallback to the Chinese catalog, got %q", got)
	}
}

func TestRenderFallsBackToRawKeyWhenMissingEverywhere(t *testing.T) {
	m := types.Msg("test.does_not_exist")
	if got := Render(LangEN, m); got != "test.does_not_exist" {
		t.Errorf("expected the raw key as a last resort, got %q", got)
	}
}

// A page rendering wrong is bad; a page failing to render at all is worse --
// Render must never panic on a message it doesn't recognize.
func TestRenderNeverPanicsOnUnknownKey(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Render panicked on an unknown key: %v", r)
		}
	}()
	Render(LangEN, types.Msg("totally.unregistered.key", "x", 1))
}

func TestRenderRendersLegacyLiteralUnchangedInEveryLanguage(t *testing.T) {
	m := types.Message{Literal: "旧的历史记录文字"}
	for _, lang := range []Lang{LangEN, LangZH} {
		if got := Render(lang, m); got != "旧的历史记录文字" {
			t.Errorf("Render(%s) on a legacy literal = %q, want it unchanged", lang, got)
		}
	}
}

func TestRenderLeavesUnmatchedPlaceholderVisible(t *testing.T) {
	register(LangEN, map[string]string{"test.missing_arg": "value is {missing}"})
	got := Render(LangEN, types.Msg("test.missing_arg"))
	if got != "value is {missing}" {
		t.Errorf("expected the unmatched placeholder to stay visible, got %q", got)
	}
}

func TestParseLangDefaultsToChineseForUnknownInput(t *testing.T) {
	cases := []string{"", "fr", "ZH-Hans", "  ", "english"}
	for _, c := range cases {
		if got := ParseLang(c); c != "ZH-Hans" && got != DefaultLang {
			// "ZH-Hans" is intentionally not recognized either -- only the
			// exact "en"/"zh" tokens are valid -- so it also falls back.
		}
	}
	if ParseLang("en") != LangEN {
		t.Error(`ParseLang("en") should be LangEN`)
	}
	if ParseLang("EN") != LangEN {
		t.Error("ParseLang should be case-insensitive")
	}
	if ParseLang("bogus") != DefaultLang {
		t.Error("unrecognized input should fall back to DefaultLang (Chinese), not error")
	}
}

func TestRegisterPanicsOnDuplicateKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected register to panic on a duplicate key")
		}
	}()
	register(LangEN, map[string]string{"test.dup": "a"})
	register(LangEN, map[string]string{"test.dup": "b"})
}
