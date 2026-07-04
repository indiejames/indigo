# indigo-spell

Spell-checking plugin for the indigo editor. Misspelled words are underlined
(undercurl in terminals that support it). Press `Shift+F` over a word to see
suggestions and dictionary-add options.

## What gets checked

The plugin is file-type aware so it doesn't flag identifiers, type names, or
other code tokens:

| File type | Extensions | What is checked |
|-----------|------------|-----------------|
| Plain text / Markdown | `.md`, `.txt`, `.rst`, `.adoc`, `.org`, (none) | Everything |
| C-style source | `.go`, `.c`, `.cpp`, `.js`, `.ts`, `.java`, `.rs`, `.swift`, … | `//` and `/* */` comments only |
| Script / config | `.py`, `.rb`, `.sh`, `.bash`, `.zsh`, `.fish`, `.toml`, `.yaml`, `.yml` | `#` comments only |
| Data files | `.json`, `.lock`, `.sum` | Nothing (skipped) |

String literals in code files are **not** currently checked. If you want a
word ignored everywhere, add it with `:spell-add`.

## Built-in dictionaries

The plugin ships with the following dictionaries compiled in:

| File | Contents | Source |
|------|----------|--------|
| `dict/en_US.aff` / `dict/en_US.dic` | ~50 000 English (US) words with full Hunspell affix rules | [wooorm/dictionaries](https://github.com/wooorm/dictionaries) (MIT), generated from [SCOWL](http://wordlist.aspell.net/) |
| `dict/software-terms.txt` | Common software and technology terms | [cspell-dicts](https://github.com/streetsidesoftware/cspell-dicts) (MIT) |
| `dict/coding-terms.txt` | Programming-specific words | [cspell-dicts](https://github.com/streetsidesoftware/cspell-dicts) (MIT) |
| `dict/computing-acronyms.txt` | Computing acronyms (API, HTTP, JSON, …) | [cspell-dicts](https://github.com/streetsidesoftware/cspell-dicts) (MIT) |

## Key bindings

| Key | Action |
|-----|--------|
| `Shift+F` (normal mode, cursor on a word) | Show fix popup with suggestions |

## Ex commands

| Command | Description |
|---------|-------------|
| `:spell-add <word>` | Add a word to your global user dictionary |
| `:spell-add-workspace <word>` | Add a word to the current workspace dictionary |

## User word files

Two plain-text word files are loaded at startup (one word per line; lines
starting with `#` are ignored):

| File | Scope |
|------|-------|
| `~/.config/indigo/spell/user.dic` | Global — applies in every project |
| `.indigo/spell.dic` (relative to editor working directory) | Workspace — checked into the project repo |

Both files are created automatically when needed. You can edit them by hand or
use the commands above.

## Adding custom word lists

Drop any number of `.txt` files (one word per line, `#` comments) into:

```
~/.config/indigo/spell/wordlists/
```

The plugin loads every file in that directory at startup. This is the easiest
way to add a domain-specific vocabulary (medical terms, company names, product
identifiers, etc.) without rebuilding the plugin.

Example:

```
# ~/.config/indigo/spell/wordlists/acme-products.txt
Acme
Anvil
Roadrunner
```

## Adding language dictionaries

To spell-check in a language other than English, place a Hunspell dictionary
pair (a `.aff` affix file and a `.dic` word file with the same base name) in:

```
~/.config/indigo/spell/dicts/
```

Example — French:

```
~/.config/indigo/spell/dicts/fr_FR.aff
~/.config/indigo/spell/dicts/fr_FR.dic
```

The plugin loads every valid `.aff`/`.dic` pair it finds there at startup. All
loaded languages are checked in parallel; a word is accepted if any checker
recognises it.

### Where to get Hunspell dictionaries

**[wooorm/dictionaries](https://github.com/wooorm/dictionaries)** (recommended)
— High-quality, consistently licensed dictionaries for dozens of languages
(Afrikaans, Arabic, Bulgarian, Catalan, Croatian, Czech, Danish, Dutch,
Finnish, French, Galician, German, Greek, Hebrew, Hungarian, Italian,
Norwegian, Polish, Portuguese, Romanian, Russian, Serbian, Slovak, Slovenian,
Spanish, Swedish, Ukrainian, and more). Each language lives under
`dictionaries/<tag>/index.aff` and `dictionaries/<tag>/index.dic`.

```sh
# Example: download French
curl -L -o ~/.config/indigo/spell/dicts/fr_FR.aff \
  https://raw.githubusercontent.com/wooorm/dictionaries/main/dictionaries/fr/index.aff
curl -L -o ~/.config/indigo/spell/dicts/fr_FR.dic \
  https://raw.githubusercontent.com/wooorm/dictionaries/main/dictionaries/fr/index.dic
```

**System Hunspell installation** — many Linux distributions and macOS (via
Homebrew) ship dictionaries in `/usr/share/hunspell/` or
`/usr/share/myspell/dicts/`. You can symlink or copy from there:

```sh
# macOS with hunspell from Homebrew
cp /opt/homebrew/share/hunspell/de_DE.aff ~/.config/indigo/spell/dicts/
cp /opt/homebrew/share/hunspell/de_DE.dic ~/.config/indigo/spell/dicts/
```

**LibreOffice extension repository** — [extensions.libreoffice.org](https://extensions.libreoffice.org/?q=&Tag%5B%5D=50)
lists spell-check extensions. Each `.oxt` file is a ZIP archive; extract it
and copy the `.aff`/`.dic` files.

### Limitations

- Only the embedded `en_US` dictionary benefits from full affix-rule expansion
  (prefixes, suffixes, compound words). Extra language dictionaries added at
  runtime are checked using the same affix rules they ship with, so derived
  forms work correctly.
- Custom word lists added via `~/.config/indigo/spell/wordlists/` are added as
  raw words without affix expansion. If a conjugated or inflected form is not
  in the list it will be flagged; add the specific form you need.
- There is no runtime UI to switch the active language. All loaded dictionaries
  are always active simultaneously.
