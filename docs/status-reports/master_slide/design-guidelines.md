# Realtek 2026 Master Template Design Guideline

Status: source.

Source deck: [`powerpoint_master.pptx`](powerpoint_master.pptx).

This guideline translates the company PowerPoint master into a designer-readable
system for Realtek Cloud status reports, executive updates, and technical
platform presentations. Use it when creating new slides or converting report
content into a deck.

## Design Intent

The master is a light, technology-focused Realtek corporate template. It should
feel like semiconductor / AI / connected-device infrastructure: clean white
content space, cool blue accents, precise diagrams, and restrained use of
futuristic imagery.

Core visual message:

- Realtek corporate identity first.
- Light technology background, not dark cyber style.
- Blue/cyan accents for system, cloud, AI, and connectivity emphasis.
- Dense technical content is acceptable, but hierarchy must stay clear.
- The template is corporate and engineering-facing, not a marketing landing
  page.

## Canvas

| Property | Value |
| --- | --- |
| Format | 16:9 widescreen |
| PowerPoint size | 10.00 in x 5.62 in |
| Suggested pixel equivalent | 1920 x 1080 |
| Safe content margin | About 0.45-0.60 in left/right for body slides |
| Footer zone | Bottom 0.25-0.35 in, reserved for copyright or slide number |

Keep content away from the bottom blue/footer band and avoid placing important
text over the decorative side imagery.

## Typography

Use the master font rules exactly:

| Language | Font |
| --- | --- |
| Traditional Chinese | Microsoft JhengHei / `微軟正黑體` |
| Simplified Chinese | Microsoft YaHei / `微软雅黑` |
| English / numbers | Arial |

Guidance:

- Use bold title weight for page headlines.
- Use regular or semibold body text; avoid decorative fonts.
- Keep bilingual title treatment consistent: Chinese title first when the deck
  is Chinese-led, English subtitle below when needed.
- Do not mix Calibri into generated slides unless editing an inherited object
  that already uses it and cannot be safely changed.

## Color System

Primary corporate palette from the master theme:

| Role | Hex |
| --- | --- |
| Realtek / primary blue | `#4A66AC` |
| Deep navy | `#242852` |
| Light tech blue | `#ACCBF9` |
| Sky blue | `#629DD1` |
| Active blue | `#297FD5` |
| Neutral steel | `#7F8FA9` |
| Cyan teal | `#5AA2AE` |
| Muted lavender gray | `#9D90A0` |
| Black text | `#000000` |
| White | `#FFFFFF` |

Secondary light palette observed in the master:

| Role | Hex |
| --- | --- |
| Cyan accent | `#3494BA` |
| Aqua accent | `#58B6C0` |
| Green-teal accent | `#75BDA7` |
| Cool gray | `#7A8C8E` |
| Soft blue gray | `#84ACB6` |
| Link green | `#6B9F25` |

Usage rules:

- Use white or very pale blue backgrounds for most content slides.
- Use primary blue/deep navy for section labels, title accents, key numbers,
  and table headers.
- Use cyan/teal only as secondary highlights, not as a full-page wash.
- Avoid warm orange/brown palettes unless showing exception/risk status.
- For charts, use two to four blue/cyan series first; add gray for baseline or
  historical context.

## Master Assets

Extracted assets are in [`assets/`](assets/).

| Asset | Use |
| --- | --- |
| `image1.png` | Main title/cover background, 1920 x 1080. |
| `image2.png`, `image3.png`, `image6.png` | Realtek logo variants. |
| `image7.jpg` | Full-slide light technology background. |
| `image4.png`, `image5.jpg`, `image8.jpg`, `image9.png`, `image12.png` | Vertical side imagery and angled technology panels. |
| `image10.jpeg`, `image13.jpeg` | Thin blue footer/header strips. |
| `image17.jpeg`, `image18.jpeg` | Quote marks or small blue decorative punctuation. |
| `image14.png`, `image15.png`, `image16.png` | PowerPoint UI examples from the master; use only when explaining chart/table styling. |

Use [`assets/media-contact-sheet.png`](assets/media-contact-sheet.png) as the
quick visual inventory before selecting assets.

Do not stretch logo assets. Preserve aspect ratio and place them on clear
backgrounds.

## Slide Families

The source deck contains these reusable layout patterns.

| Layout | Purpose | Design Notes |
| --- | --- | --- |
| Cover / title slide | Formal report opening | Large Realtek logo, light AI/semiconductor background, centered-left title block, name/date, copyright footer. |
| Thank-you / QR end page | Closing | Minimal closing page with Realtek identity and `www.realtek.com`. |
| Agenda / chapter | Section navigation | Use large chapter heading and short agenda list; keep decorative imagery secondary. |
| Chapter title | Major section break | Use one title and one subtitle; avoid tables or dense content. |
| One-content body | Technical explanation | Use a strong title, one main proof object, and a short text block. |
| Two-content body | Comparison or two-track narrative | Left/right columns should be balanced; use for current-vs-target, before/after, architecture vs evidence. |
| Table layout | KPI / schedule / readiness matrix | Blue header row, light body cells, clear row grouping, compact footnotes. |
| Simplified Chinese body | CN-localized version | Use Microsoft YaHei for Simplified Chinese content. |

## Composition Rules

- Make the slide title a claim, not a label. Example: "Staging is live, but
  production readiness still depends on versioning and backup evidence."
- Reserve the largest type for covers and chapter pages only.
- Body slides should have one dominant proof object: architecture diagram,
  timeline, readiness matrix, screenshot, or KPI table.
- Avoid floating cards inside cards. Use table bands, callout strips, or
  full-width content zones instead.
- Keep page numbers and copyright in the footer area.
- Use the left or right angled imagery as a framing device; do not put critical
  labels on top of it.
- Use screenshots only when they prove an operation. Pair each screenshot with
  a short caption and evidence note.

## Chart And Table Style

Presentation rule: if a slide has important numbers, start with a chart or
visual encoding, not a number table. Tables are for audit detail, exact lookup,
or action tracking; charts are for executive understanding.

Charts:

- Use Realtek blues first: `#4A66AC`, `#629DD1`, `#297FD5`, `#5AA2AE`.
- Use gray for baseline/reference series.
- Keep chart backgrounds white or transparent.
- Label axes directly when possible; avoid decorative legends when a direct
  label is clearer.
- Use callouts sparingly for the one number or trend management must remember.
- Use timeline, Gantt, or milestone-lane visuals for schedules.
- Use progress/bullet charts for current-vs-target quantities.
- Use line charts for weekly movement and bar charts for service/milestone
  comparison.

Tables:

- Use dark or medium blue header rows.
- Use alternating pale blue/white rows only when the table is dense.
- Keep borders light gray/blue-gray.
- Use status labels consistently: `PASS`, `FAIL`, `SKIP`, `BLOCKED`,
  `not verified`, `at risk`.
- Keep risk/status colors restrained: blue/gray for neutral, green for passed,
  amber for risk, red only for failure/blocker.

## Realtek Cloud Report Mapping

Recommended slide sequence for a status-report deck:

| Section | Master pattern |
| --- | --- |
| Cover / core message | Cover / title slide |
| Executive summary | One-content body with status summary table |
| Schedule path | Timeline, Gantt, or milestone-lane chart; table only as backup detail |
| Loading test readiness | Table layout |
| Cloud relationship | Three-layer diagram: Realtek Platform Root -> Brand Cloud -> end users/devices |
| Portal web / digital marketing | Funnel or content-map visual: traffic -> engagement -> CTA -> lead -> sales follow-up |
| Threat model / cyber security | STRIDE heatmap or risk matrix plus concise mitigation/evidence status |
| Cloud architecture | One-content body with architecture proof object |
| WebRTC / video storage | Two-content body or readiness matrix |
| MQTT / device shadow | Two-lane diagram or topic-surface table |
| Security / PKI | Trust-chain diagram plus management matrix |
| Linode deployment | Table layout plus runtime shape diagram |
| Operation screenshots | One-content or two-content body |
| Risks / decisions | Table layout |
| Appendix | Dense table layout, small but readable |

## AI Generation Rules

When an AI or LLM generates slides from this master:

- Start from the master style; do not invent a new corporate look.
- Keep `微軟正黑體` for Traditional Chinese and Arial for English.
- Use the extracted background/logo assets from this directory.
- Keep slide content in Traditional Chinese by default.
- Preserve literal API names, repository names, endpoint paths, and status
  labels in English.
- Never include secrets, bearer tokens, private keys, raw customer data, or raw
  media that is not approved for the deck audience.
- For status reports, prefer evidence tables and diagrams over marketing copy.

## Common Mistakes To Avoid

- Turning the template into a dark cybersecurity deck.
- Using generic SaaS gradient cards unrelated to the Realtek master.
- Overusing the full background image on every dense slide.
- Placing text over busy side imagery.
- Mixing Calibri/Arial/Chinese fonts inconsistently.
- Treating screenshots as decoration instead of operational evidence.
- Claiming WebRTC, storage, PKI, MQTT, or production readiness without evidence.
- Claiming autoscaling, elastic cloud, or dynamic scaling implementation for the
  August release. It is acceptable to show scaling-ready architecture
  boundaries when evidence supports them, but implementation remains deferred
  until after the loading test.
