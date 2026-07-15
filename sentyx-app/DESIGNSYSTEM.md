# Sentyx Design System

Package: `com.sentyx.app.core.designsystem` (commonMain). Light-only, warm cream
palette matching `design/Sentyx.dc.html`. Wrap the app in `SentyxTheme { }`.
Everything is drawn in code — no fonts, images, or resources.

## Theme

| Symbol | Signature | Description |
|---|---|---|
| `SentyxTheme` | `@Composable SentyxTheme(content: @Composable () -> Unit)` | MaterialTheme wrapper with the light color scheme built from `SxColors`. |
| `monoFamily` | `val monoFamily: FontFamily` | `FontFamily.Monospace` token for IBM Plex Mono roles (codes, timestamps). |

## Tokens

### `SxColors` (object)
Surfaces: `Bg` #F4F0E9, `Card` #FBF8F2, `Border` #E7E0D2, `Divider` #EDE7DA, `Sand` #D8CEBB.
Ink/text: `Ink` #2B271F, `InkSecondary` #6E6656, `Muted` #8A8271, `Faint` #A69E8B, `Hint` #B0A892, `Chevron` #C4BBA8.
Accents: `Gold` #C9AE77, `Bronze` #9A7B3C, `Green` #2F7C4A, `Red` #B23A2E, `RedDeep` #9A2E22, `Amber` #C08A3E, `AmberDeep` #8A5A12.
Status bg: `UrgentBg` #F3D9D2, `AttentionBg` #E9DCC4, `RoutineBg` #EAE4D8.
Hover: `HoverLight` #EDE7DA, `HoverDark` #463F31.
Severity nodes: `SeverityUrgentNode` #B23A2E, `SeverityAttentionNode` #C08A3E, `SeverityRoutineNode` #B8AD97.
Severity fg: `SeverityUrgentFg`, `SeverityAttentionFg`, `SeverityRoutineFg` (#7A7060).
Helpers: `OnInk` (= Bg, for text on dark), `White`.

### `SxDimens` (object)
`ScreenPadding` 22.dp, `OnboardingPadding` 26.dp, `CardRadius` 16.dp, `CardRadiusSmall` 14.dp,
`ButtonRadius` 14.dp, `ButtonRadiusSmall` 13.dp, `InputRadius` 12.dp, `ChipRadius` 999.dp,
`SheetRadius` 22.dp, `PrimaryButtonVPadding` 16.dp, `SecondaryButtonVPadding` 15.dp,
`ListRowVPadding` 14.dp, `ListRowHPadding` 16.dp, `ToggleWidth` 38.dp, `ToggleHeight` 22.dp,
`ToggleKnob` 18.dp, `DragHandleWidth` 38.dp, `DragHandleHeight` 4.dp.

### Severity helpers
| Symbol | Signature | Description |
|---|---|---|
| `severityColors` | `fun severityColors(severity: Severity): SeverityColors` | Returns `SeverityColors(bg, fg, node)` for a severity. |
| `severityLabel` | `fun severityLabel(severity: Severity): String` | Capitalized label ("Urgent" / "Attention" / "Routine"). |
| `SeverityColors` | `data class SeverityColors(bg: Color, fg: Color, node: Color)` | Pill background, text, and timeline-dot colors. |

## Buttons & links

| Symbol | Signature | Description |
|---|---|---|
| `SxPrimaryButton` | `@Composable SxPrimaryButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier)` | Full-width dark ink pill, primary CTA. |
| `SxSecondaryButton` | `@Composable SxSecondaryButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier)` | Full-width transparent pill with 1px sand border. |
| `SxTextLink` | `@Composable SxTextLink(text: String, onClick: () -> Unit, modifier: Modifier = Modifier)` | Bronze bold inline text action. |

## Cards & lists

| Symbol | Signature | Description |
|---|---|---|
| `SxCard` | `@Composable SxCard(modifier: Modifier = Modifier, onClick: (() -> Unit)? = null, content: @Composable () -> Unit)` | Cream card, 1px border, 16dp radius, 14dp inner padding; optionally clickable. |
| `SxListGroup` | `@Composable SxListGroup(modifier: Modifier = Modifier, content: @Composable () -> Unit)` | Bordered, clipped container for a stack of `SxListRow`s. |
| `SxListRow` | `@Composable SxListRow(label: String, modifier: Modifier = Modifier, value: String? = null, onClick: (() -> Unit)? = null, showChevron: Boolean = false, showDivider: Boolean = true, labelColor: Color = SxColors.Ink, valueColor: Color = SxColors.Muted, trailing: (@Composable () -> Unit)? = null)` | One settings-list row: label, optional value/chevron/trailing slot, 1px bottom divider (set `showDivider=false` on the last row). |

## Controls

| Symbol | Signature | Description |
|---|---|---|
| `SxToggle` | `@Composable SxToggle(checked: Boolean, onToggle: (Boolean) -> Unit, modifier: Modifier = Modifier)` | 38x22 pill toggle, animated; green on / sand off, white 18dp knob. |
| `SxToggleRow` | `@Composable SxToggleRow(title: String, checked: Boolean, onToggle: (Boolean) -> Unit, modifier: Modifier = Modifier, subtitle: String? = null)` | Row with title (+ optional subtitle) and a trailing `SxToggle`. |
| `SxChip` | `@Composable SxChip(label: String, selected: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier)` | Pill chip: selected = dark bg/cream text; unselected = cream bg / muted text / border. |

## Badges & status

| Symbol | Signature | Description |
|---|---|---|
| `SeverityBadge` | `@Composable SeverityBadge(severity: Severity, modifier: Modifier = Modifier)` | Small pill using the severity's bg/fg + capitalized label. |
| `SeverityDot` | `@Composable SeverityDot(severity: Severity, modifier: Modifier = Modifier, size: Dp = 11.dp)` | Small filled dot in the severity's node color. |
| `StatusPill` | `@Composable StatusPill(text: String, color: Color, bg: Color, modifier: Modifier = Modifier, pulsing: Boolean = false)` | Rounded pill: small (optionally pulsing) dot + bold 11sp label. |

## Headers

| Symbol | Signature | Description |
|---|---|---|
| `SxSectionHeader` | `@Composable SxSectionHeader(text: String, modifier: Modifier = Modifier)` | 12sp bold uppercase letter-spaced faint label; 20dp top / 8dp bottom spacing baked in. |
| `SxBackHeader` | `@Composable SxBackHeader(title: String, onBack: () -> Unit, modifier: Modifier = Modifier)` | "‹" back button + 22sp extrabold title; used by every sub-screen. |

## Inputs

| Symbol | Signature | Description |
|---|---|---|
| `SxTextFieldDisplay` | `@Composable SxTextFieldDisplay(label: String, value: String, modifier: Modifier = Modifier, masked: Boolean = false)` | Display-only labeled input look (12sp bold label above a bordered cream box). `masked` renders bullets. No editing. |

## Overlays

| Symbol | Signature | Description |
|---|---|---|
| `SxBottomSheet` | `@Composable SxBottomSheet(onDismiss: () -> Unit, modifier: Modifier = Modifier, content: @Composable () -> Unit)` | Full-screen scrim (dismiss on tap) + bottom sheet with 22dp top radius and drag handle; taps inside are consumed. `content` is a column body. |
| `SxToastBar` | `@Composable SxToastBar(toast: ToastData, modifier: Modifier = Modifier)` | Dark floating banner: pulsing tone dot + title/subtitle + gold CTA; tapping invokes `toast.onTap`. Caller positions it as a top overlay. |
| `toastToneColor` | `fun toastToneColor(tone: ToastTone): Color` | Info→bronze, Success→green, Urgent→red. |

## Feedback & progress

| Symbol | Signature | Description |
|---|---|---|
| `SxEmptyState` | `@Composable SxEmptyState(emoji: String, title: String, body: String, modifier: Modifier = Modifier)` | Centered emoji + extrabold title + muted body. |
| `SxSpinner` | `@Composable SxSpinner(modifier: Modifier = Modifier, size: Dp = 44.dp, strokeWidth: Dp = 3.dp)` | Border-track ring with a bronze top arc, rotating 1s/turn. |
| `SxProgressBar` | `@Composable SxProgressBar(pct: Float, color: Color, modifier: Modifier = Modifier, height: Dp = 5.dp)` | Divider-track bar with a colored fill; `pct` is 0f..1f (clamped). |

## Graphics

| Symbol | Signature | Description |
|---|---|---|
| `PulsingDot` | `@Composable PulsingDot(color: Color, modifier: Modifier = Modifier, size: Dp = 8.dp)` | Dot pulsing alpha 1→0.3 on a ~1.6s loop. |
| `StripedThumb` | `@Composable StripedThumb(modifier: Modifier = Modifier, overlay: (@Composable () -> Unit)? = null)` | Diagonal 45° striped placeholder for video thumbs; optional centered `overlay`. |
| `SentyxLogo` | `@Composable SentyxLogo(modifier: Modifier = Modifier, size: Dp = 60.dp)` | Two-circle gold mark (solid inner + dashed outer) on a dark rounded-square tile; `size` is the tile edge. |

## Notes

- `pct` in `SxProgressBar` is a fraction (0f..1f), not a percentage.
- `SxListRow` divider is per-row; suppress it on the last row via `showDivider=false`.
- `SxBottomSheet` fills the whole screen (scrim + sheet); place it at the top of the container's z-order.
- DM Sans is approximated by the platform sans-serif; use `monoFamily` where the design specifies IBM Plex Mono.
