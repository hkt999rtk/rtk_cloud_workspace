from __future__ import annotations

import json
import math
import os
from datetime import date, datetime, timezone
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from PIL import Image, ImageDraw, ImageFont


ROOT = Path(__file__).resolve().parents[2]
REPORT_DATE = date.today().isoformat()
SNAPSHOT_TIME_UTC = datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
OUT_DIR = ROOT / ".artifacts" / "status-reports" / REPORT_DATE
FIG_DIR = OUT_DIR / "figures"
DOCX_PATH = OUT_DIR / "realtek_video_iot_cloud_status_report.docx"
PORTAL_WEB_URL = "https://webtest.mgmeet.io"
PORTAL_WEB_SCREENSHOT = FIG_DIR / "portal-webtest-home-hero.png"
PORTAL_WEB_FALLBACK_IMAGE = ROOT / "repos/rtk_cloud_frontend/static/assets/connectplus-hero-corporate-v2.jpg"
REPORT_LANGUAGE = "繁體中文"
CORE_MESSAGE = (
    "這個 Cloud 的存在目的，是輔助各單位以 Bottom-up Business 方式推動 AmebaPRO / IoT module "
    "銷售：讓使用者、開發者與客戶 PoC 團隊可以直接使用雲端 onboarding、SDK/App、OTA、Video、"
    "MQTT 與管理工具，縮短從硬體模組評估到解決方案導入的距離。它要提供的是方便導入、可展示、可驗證，"
    "同時具備安全管理的網路架構，讓模組銷售能從硬體規格延伸成完整解決方案。"
)
CURRENT_STATUS_SUMMARY = [
    ["Deployment", "Linode staging 已可作為 demo / 驗證環境使用，並以 public HTTPS health check 作為狀態證據。", "仍需補齊 release version、backup/restore 與 production-like sign-off。"],
    ["Product / demo evidence", "Admin、SDK sample flow、Connect+ architecture 素材已可支撐端到端展示。", "下一步要把 demo flow 連到 loading test 與 customer PoC 指標。"],
    ["Operations readiness", "Account Manager、Video Cloud、Admin 分工已清楚，service health 可被報告化。", "正式商用後的 SLA、support owner、incident response 與持續維運人力仍需確認。"],
    ["Next milestone", "2026-08-01 要完成 50,000 IoT devices + 5,000 video cameras loading test。", "8 月進 alpha test（含 SDK），9 月進 beta test（含 SDK 與 pilot customer），再進 public。"],
]

FONT_REG = "/System/Library/Fonts/STHeiti Light.ttc"
FONT_BOLD = "/System/Library/Fonts/STHeiti Medium.ttc"

COLORS = {
    "navy": "17324D",
    "teal": "0F766E",
    "cyan": "DDF7F3",
    "blue": "DCEBFF",
    "green": "E3F5E8",
    "amber": "FFF2CC",
    "orange": "F59E0B",
    "gray": "F3F6F8",
    "line": "8AA1B4",
    "dark": "233241",
    "muted": "5B6773",
    "white": "FFFFFF",
}

DESIGN_MATERIALS = [
    {
        "key": "admin_overview",
        "title": "Admin Fleet Health Overview",
        "path": ROOT / "repos/rtk_cloud_admin/docs/assets/webui-design/customer-overview.png",
        "caption": "圖 7：Admin Customer View - Fleet Health Overview 操作畫面",
        "source": "rtk_cloud_admin/docs/assets/webui-design/customer-overview.png",
        "purpose": "管理者與客戶可一眼看到 online rate、attention queue、health distribution。",
    },
    {
        "key": "admin_devices",
        "title": "Admin Devices + Detail Drawer",
        "path": ROOT / "repos/rtk_cloud_admin/docs/assets/webui-design/customer-devices.png",
        "caption": "圖 8：Admin Devices list 與右側 device detail drawer",
        "source": "rtk_cloud_admin/docs/assets/webui-design/customer-devices.png",
        "purpose": "展示 fleet manager 如何搜尋、過濾、查看 health/source facts、stream status 與 provisioning action。",
    },
    {
        "key": "admin_firmware_ota",
        "title": "Admin Firmware & OTA",
        "path": ROOT / "repos/rtk_cloud_admin/docs/assets/webui-design/customer-firmware-ota.png",
        "caption": "圖 9：Firmware & OTA 操作畫面",
        "source": "rtk_cloud_admin/docs/assets/webui-design/customer-firmware-ota.png",
        "purpose": "說明 firmware distribution、rollout progress、risk queue 如何對應售後維運。",
    },
    {
        "key": "admin_stream_health",
        "title": "Admin Stream Health",
        "path": ROOT / "repos/rtk_cloud_admin/docs/assets/webui-design/customer-stream-health.png",
        "caption": "圖 10：Stream Health 操作畫面",
        "source": "rtk_cloud_admin/docs/assets/webui-design/customer-stream-health.png",
        "purpose": "說明 video stream success rate、request volume、per-device failure risk 的操作視角。",
    },
    {
        "key": "sample_screen_flows",
        "title": "Sample App Screen Flow",
        "path": ROOT / "repos/rtk_cloud_client/docs/mockups/sample-ops-lab-screen-flows.png",
        "caption": "圖 11：SDK / Sample App 操作流程與 debug evidence",
        "source": "rtk_cloud_client/docs/mockups/sample-ops-lab-screen-flows.png",
        "purpose": "展示 environment setup、add device/provision、device configuration、camera monitor、debug report。",
    },
    {
        "key": "frontend_architecture",
        "title": "Connect+ Architecture Visual",
        "path": ROOT / "repos/rtk_cloud_frontend/static/assets/connectplus-architecture-diagram-corporate-v2.jpg",
        "caption": "圖 12：Frontend Connect+ architecture visual",
        "source": "rtk_cloud_frontend/static/assets/connectplus-architecture-diagram-corporate-v2.jpg",
        "purpose": "作為對外產品敘事的 architecture / ecosystem 圖像素材。",
    },
]

LINODE_ENDPOINTS = [
    {
        "component": "Video Cloud runtime",
        "url": "https://video-cloud-staging.realtekconnect.com/healthz",
        "kind": "health",
        "expected": "status=ok",
    },
    {
        "component": "Video Cloud runtime",
        "url": "https://video-cloud-staging.realtekconnect.com/version",
        "kind": "version",
        "expected": "ApiVersion/AppVersion",
    },
    {
        "component": "Account Manager API",
        "url": "https://account-manager.video-cloud-staging.realtekconnect.com/v1/health",
        "kind": "health",
        "expected": "status=ok",
    },
    {
        "component": "Admin dashboard",
        "url": "https://admin.video-cloud-staging.realtekconnect.com/healthz",
        "kind": "health",
        "expected": "ok",
    },
    {
        "component": "Admin dashboard",
        "url": "https://admin.video-cloud-staging.realtekconnect.com/api/service-health",
        "kind": "service-health",
        "expected": "Account Manager, Video Cloud, SQLite ok",
    },
]


def rgb(hex_color: str) -> tuple[int, int, int]:
    hex_color = hex_color.lstrip("#")
    return tuple(int(hex_color[i : i + 2], 16) for i in (0, 2, 4))


def font(size: int, bold: bool = False) -> ImageFont.FreeTypeFont:
    return ImageFont.truetype(FONT_BOLD if bold else FONT_REG, size)


def wrap_text(draw: ImageDraw.ImageDraw, text: str, fnt: ImageFont.FreeTypeFont, max_width: int) -> list[str]:
    lines: list[str] = []
    current = ""
    for ch in text:
        trial = current + ch
        if draw.textbbox((0, 0), trial, font=fnt)[2] <= max_width:
            current = trial
        else:
            if current:
                lines.append(current)
            current = ch
    if current:
        lines.append(current)
    return lines


def centered_multiline(
    draw: ImageDraw.ImageDraw,
    box: tuple[int, int, int, int],
    text: str,
    fnt: ImageFont.FreeTypeFont,
    fill: str,
    line_spacing: int = 8,
) -> None:
    x1, y1, x2, y2 = box
    lines = []
    for part in text.split("\n"):
        lines.extend(wrap_text(draw, part, fnt, x2 - x1 - 36))
    heights = [draw.textbbox((0, 0), line, font=fnt)[3] - draw.textbbox((0, 0), line, font=fnt)[1] for line in lines]
    total_h = sum(heights) + line_spacing * (len(lines) - 1)
    y = y1 + (y2 - y1 - total_h) / 2
    for line, h in zip(lines, heights):
        w = draw.textbbox((0, 0), line, font=fnt)[2]
        draw.text((x1 + (x2 - x1 - w) / 2, y), line, font=fnt, fill=rgb(fill))
        y += h + line_spacing


def rounded_box(
    draw: ImageDraw.ImageDraw,
    box: tuple[int, int, int, int],
    fill: str,
    outline: str = "8AA1B4",
    radius: int = 24,
    width: int = 3,
) -> None:
    draw.rounded_rectangle(box, radius=radius, fill=rgb(fill), outline=rgb(outline), width=width)


def arrow(draw: ImageDraw.ImageDraw, start: tuple[int, int], end: tuple[int, int], color: str = "8AA1B4", width: int = 5) -> None:
    draw.line([start, end], fill=rgb(color), width=width)
    ang = math.atan2(end[1] - start[1], end[0] - start[0])
    size = 16
    p1 = (end[0] - size * math.cos(ang - math.pi / 6), end[1] - size * math.sin(ang - math.pi / 6))
    p2 = (end[0] - size * math.cos(ang + math.pi / 6), end[1] - size * math.sin(ang + math.pi / 6))
    draw.polygon([end, p1, p2], fill=rgb(color))


def save_image(name: str, draw_fn, size: tuple[int, int] = (1800, 1000)) -> Path:
    path = FIG_DIR / name
    img = Image.new("RGB", size, rgb("FFFFFF"))
    draw = ImageDraw.Draw(img)
    draw_fn(draw, size)
    img.save(path, quality=95)
    return path


def probe_json_or_text(url: str, timeout: int = 12) -> tuple[str, str]:
    req = Request(url, headers={"User-Agent": "rtk-status-report-builder/1.0"})
    try:
        with urlopen(req, timeout=timeout) as resp:
            raw = resp.read(6000).decode("utf-8", errors="replace").strip()
            status = getattr(resp, "status", 200)
            return "PASS", f"HTTP {status}: {raw}"
    except HTTPError as exc:
        raw = exc.read(1200).decode("utf-8", errors="replace").strip()
        return "FAIL", f"HTTP {exc.code}: {raw}"
    except (TimeoutError, URLError, OSError) as exc:
        return "BLOCKED", f"{type(exc).__name__}: {exc}"


def collect_linode_health() -> list[dict[str, str]]:
    rows = []
    for endpoint in LINODE_ENDPOINTS:
        result, observed = probe_json_or_text(endpoint["url"])
        rows.append(
            {
                "component": endpoint["component"],
                "check": endpoint["kind"],
                "url": endpoint["url"],
                "expected": endpoint["expected"],
                "result": result,
                "observed": normalize_observed(endpoint["kind"], observed),
            }
        )
    return rows


def read_linode_token() -> str | None:
    token_keys = {"LINODE_TOKEN", "LINODE_CLI_TOKEN", "LINODE_API_TOKEN", "LINODE_API_KEY", "LINODE_PAT"}
    for key in token_keys:
        value = os.environ.get(key)
        if value:
            return value.strip().strip('"').strip("'")
    env_path = Path.home() / ".env"
    if not env_path.exists():
        return None
    for raw_line in env_path.read_text(encoding="utf-8", errors="ignore").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key.strip() in token_keys:
            token = value.strip().strip('"').strip("'")
            return token or None
    return None


def linode_api_get(path: str, token: str, timeout: int = 12) -> tuple[str, object]:
    req = Request(
        f"https://api.linode.com/v4{path}",
        headers={
            "Authorization": f"Bearer {token}",
            "User-Agent": "rtk-status-report-builder/1.0",
        },
    )
    try:
        with urlopen(req, timeout=timeout) as resp:
            raw = resp.read(12000).decode("utf-8", errors="replace")
            return "PASS", json.loads(raw)
    except HTTPError as exc:
        return "FAIL", {"error": f"HTTP {exc.code}"}
    except (TimeoutError, URLError, OSError, json.JSONDecodeError) as exc:
        return "BLOCKED", {"error": f"{type(exc).__name__}: {exc}"}


def money(value: object) -> str:
    try:
        return f"${float(value):,.2f}"
    except (TypeError, ValueError):
        return "n/a"


def estimate_monthly_run_rate(uninvoiced: object, invoice_date: object) -> tuple[str, str, str]:
    try:
        amount = float(uninvoiced)
        if not invoice_date:
            raise ValueError("missing invoice date")
        start = datetime.fromisoformat(str(invoice_date).replace("Z", "+00:00"))
        if start.tzinfo is None:
            start = start.replace(tzinfo=timezone.utc)
        now = datetime.now(timezone.utc)
        elapsed_days = max((now - start).total_seconds() / 86400, 1.0)
        monthly = amount / elapsed_days * 30
        period = f"{start.date().isoformat()} to {now.date().isoformat()} ({elapsed_days:.1f} days)"
        return money(monthly), period, f"{money(amount)} current uninvoiced / {elapsed_days:.1f} days x 30"
    except (TypeError, ValueError):
        return "n/a", "n/a", "insufficient billing history for monthly run-rate estimate"


def collect_linode_billing() -> dict[str, object]:
    token = read_linode_token()
    if not token:
        return {
            "status": "unavailable",
            "timestamp": SNAPSHOT_TIME_UTC,
            "summary": "Linode account API token not found. Add LINODE_TOKEN, LINODE_CLI_TOKEN, LINODE_API_TOKEN, LINODE_API_KEY, or LINODE_PAT to ~/.env for billing evidence.",
            "currentBalance": "n/a",
            "uninvoiced": "n/a",
            "estimatedMonthlyRunRate": "n/a",
            "estimatePeriod": "n/a",
            "estimateBasis": "billing API unavailable",
            "lastInvoice": "n/a",
            "lastInvoiceDate": "n/a",
        }

    account_status, account = linode_api_get("/account", token)
    invoice_status, invoices = linode_api_get("/account/invoices?page=1", token)
    if account_status != "PASS":
        return {
            "status": account_status,
            "timestamp": SNAPSHOT_TIME_UTC,
            "summary": str(account.get("error", "Linode account billing query failed")),
            "currentBalance": "n/a",
            "uninvoiced": "n/a",
            "estimatedMonthlyRunRate": "n/a",
            "estimatePeriod": "n/a",
            "estimateBasis": "billing API unavailable",
            "lastInvoice": "n/a",
            "lastInvoiceDate": "n/a",
        }

    invoice = {}
    if invoice_status == "PASS" and isinstance(invoices, dict) and invoices.get("data"):
        invoice = invoices["data"][0] or {}
    estimated_monthly, estimate_period, estimate_basis = estimate_monthly_run_rate(account.get("balance_uninvoiced"), invoice.get("date"))

    return {
        "status": "available",
        "timestamp": SNAPSHOT_TIME_UTC,
        "summary": "Current Linode account billing snapshot from read-only account API query.",
        "currentBalance": money(account.get("balance")),
        "uninvoiced": money(account.get("balance_uninvoiced")),
        "estimatedMonthlyRunRate": estimated_monthly,
        "estimatePeriod": estimate_period,
        "estimateBasis": estimate_basis,
        "lastInvoice": money(invoice.get("total")) if invoice else "n/a",
        "lastInvoiceDate": invoice.get("date", "n/a") if invoice else "n/a",
    }


def normalize_observed(kind: str, observed: str) -> str:
    prefix, _, body = observed.partition(": ")
    if kind == "version" and body:
        try:
            parsed = json.loads(body)
            return f"{prefix}: ApiVersion={parsed.get('ApiVersion', 'unknown')}, AppVersion={parsed.get('AppVersion', 'unknown')}"
        except json.JSONDecodeError:
            return observed
    if kind == "service-health" and body:
        try:
            parsed = json.loads(body)
            if isinstance(parsed, list):
                parts = [f"{item.get('name', 'unknown')}={item.get('status', 'unknown')}" for item in parsed]
                return f"{prefix}: " + ", ".join(parts)
        except json.JSONDecodeError:
            return observed
    return observed


def fig_product_to_kpi() -> Path:
    def draw_fn(draw: ImageDraw.ImageDraw, size: tuple[int, int]) -> None:
        title = font(46, True)
        body = font(28)
        draw.text((70, 55), "AmebaPRO 到商業 KPI 的路徑", font=title, fill=rgb("17324D"))
        steps = [
            ("AmebaPRO\nModule", "硬體能力與\nVideo/IoT 基礎"),
            ("SDK / App", "降低客戶\n開發門檻"),
            ("Cloud\nOnboarding", "快速進入\nPoC 驗證"),
            ("Video / OTA\nService", "形成可持續\n管理服務"),
            ("Customer PoC", "縮短導入\n決策時間"),
            ("Design-in /\nRevenue", "提高採用率與\n商業機會"),
        ]
        x = 70
        y = 300
        w = 250
        h = 220
        fills = ["DDF7F3", "DCEBFF", "E3F5E8", "FFF2CC", "DCEBFF", "DDF7F3"]
        for i, ((head, sub), fill) in enumerate(zip(steps, fills)):
            box = (x + i * 280, y, x + i * 280 + w, y + h)
            rounded_box(draw, box, fill, "8AA1B4")
            centered_multiline(draw, (box[0] + 12, box[1] + 22, box[2] - 12, box[1] + 105), head, font(31, True), "17324D")
            centered_multiline(draw, (box[0] + 18, box[1] + 112, box[2] - 18, box[3] - 18), sub, body, "233241")
            if i < len(steps) - 1:
                arrow(draw, (box[2] + 14, y + h // 2), (box[2] + 48, y + h // 2))
        draw.rounded_rectangle((95, 705, 1705, 860), radius=30, fill=rgb("F3F6F8"), outline=rgb("8AA1B4"), width=3)
        centered_multiline(
            draw,
            (130, 720, 1670, 845),
            "重點：Cloud 讓模組銷售從硬體規格競爭，延伸到完整解決方案與導入效率的競爭。",
            font(33, True),
            "17324D",
        )

    return save_image("01_product_to_kpi.png", draw_fn)


def fig_kpi_map() -> Path:
    def draw_fn(draw: ImageDraw.ImageDraw, size: tuple[int, int]) -> None:
        draw.text((70, 45), "技術成果與商業 KPI 對照", font=font(46, True), fill=rgb("17324D"))
        left_items = ["Cloud 可部署", "Device 可上線", "SDK 可整合", "OTA 可管理", "Video 可連線", "Admin 可觀測"]
        right_items = ["縮短 PoC 時間", "提高 AmebaPRO 採用率", "降低客戶導入門檻", "支援 Brand Cloud / Private Cloud", "形成維運與服務機會"]
        rounded_box(draw, (105, 170, 775, 835), "DCEBFF", "8AA1B4")
        rounded_box(draw, (1025, 170, 1695, 835), "E3F5E8", "8AA1B4")
        centered_multiline(draw, (125, 190, 755, 250), "技術 KPI", font(36, True), "17324D")
        centered_multiline(draw, (1045, 190, 1675, 250), "商業 KPI", font(36, True), "17324D")
        for i, item in enumerate(left_items):
            yy = 295 + i * 80
            draw.ellipse((155, yy + 8, 178, yy + 31), fill=rgb("0F766E"))
            draw.text((200, yy), item, font=font(29, True), fill=rgb("233241"))
        for i, item in enumerate(right_items):
            yy = 320 + i * 94
            draw.ellipse((1075, yy + 8, 1098, yy + 31), fill=rgb("F59E0B"))
            draw.text((1120, yy), item, font=font(29, True), fill=rgb("233241"))
        arrow(draw, (800, 500), (1000, 500), "F59E0B", 8)
        centered_multiline(draw, (795, 555, 1005, 650), "驗證後\n轉化", font(30, True), "17324D")

    return save_image("02_kpi_map.png", draw_fn)


def fig_three_layer() -> Path:
    def draw_fn(draw: ImageDraw.ImageDraw, size: tuple[int, int]) -> None:
        draw.text((70, 45), "三層次 Cloud 架構", font=font(46, True), fill=rgb("17324D"))
        boxes = [
            ((325, 160, 1475, 320), "Root / Realtek Platform\n平台根管理、商業政策、全域治理", "DDF7F3"),
            ((405, 405, 1395, 565), "Brand Cloud / Customer Tenant\n品牌雲、客戶租戶、私有雲或 Realtek-operated cloud", "DCEBFF"),
            ((485, 650, 1315, 850), "End User / Devices\n終端使用者管理多台裝置：camera、sensor、gateway", "FFF2CC"),
        ]
        for box, text, fill in boxes:
            rounded_box(draw, box, fill, "8AA1B4", 28, 4)
            centered_multiline(draw, box, text, font(34, True), "17324D")
        arrow(draw, (900, 324), (900, 400), "8AA1B4", 7)
        arrow(draw, (900, 569), (900, 645), "8AA1B4", 7)
        draw.text((555, 900), "資料、權限與維運責任依層次切分，避免客戶、品牌與平台治理混在一起。", font=font(27), fill=rgb("5B6773"))

    return save_image("03_three_layer.png", draw_fn)


def fig_linode() -> Path:
    def draw_fn(draw: ImageDraw.ImageDraw, size: tuple[int, int]) -> None:
        draw.text((70, 45), "Linode Fullset Deployment 現況", font=font(46, True), fill=rgb("17324D"))
        edge = (680, 160, 1120, 270)
        rounded_box(draw, edge, "F3F6F8")
        centered_multiline(draw, edge, "Reverse Proxy / TLS\nDNS、HTTPS、路由", font(27, True), "17324D")
        services = [
            ((120, 390, 455, 560), "Frontend / Website\n產品介紹、文件入口", "DDF7F3"),
            ((530, 390, 865, 560), "Admin Dashboard\nfleet、health、audit", "DCEBFF"),
            ((940, 390, 1275, 560), "Account Manager\nusers、orgs、brand cloud", "E3F5E8"),
            ((1350, 390, 1685, 560), "Video Cloud\nvideo、OTA、telemetry", "FFF2CC"),
        ]
        for box, label, fill in services:
            rounded_box(draw, box, fill)
            centered_multiline(draw, box, label, font(25, True), "17324D")
            arrow(draw, (900, 275), ((box[0] + box[2]) // 2, 385), "8AA1B4", 4)
        infra = [
            ((360, 735, 650, 865), "PostgreSQL\naccount / video metadata", "F3F6F8"),
            ((755, 735, 1045, 865), "Object Storage\nmedia / firmware", "F3F6F8"),
            ((1150, 735, 1440, 865), "EMQX / MQTT\nIoT transport", "F3F6F8"),
        ]
        for box, label, fill in infra:
            rounded_box(draw, box, fill)
            centered_multiline(draw, box, label, font(24, True), "17324D")
        arrow(draw, (1108, 565), (505, 730), "8AA1B4", 4)
        arrow(draw, (1518, 565), (900, 730), "8AA1B4", 4)
        arrow(draw, (1518, 565), (1295, 730), "8AA1B4", 4)

    return save_image("04_linode_fullset.png", draw_fn)


def fig_onboarding() -> Path:
    def draw_fn(draw: ImageDraw.ImageDraw, size: tuple[int, int]) -> None:
        draw.text((70, 45), "Device Onboarding / Provisioning Flow", font=font(46, True), fill=rgb("17324D"))
        steps = [
            ("App / SDK", "登入、掃描 Claim Token"),
            ("Account Manager", "resolve claim、綁定 user/org/device"),
            ("Cross-service Worker", "發送 provisioning intent"),
            ("Video Cloud", "activation、token、runtime"),
            ("Device Online", "telemetry / video / OTA"),
        ]
        x = 95
        y = 340
        w = 290
        h = 210
        fills = ["DDF7F3", "E3F5E8", "F3F6F8", "FFF2CC", "DCEBFF"]
        for i, ((head, sub), fill) in enumerate(zip(steps, fills)):
            box = (x + i * 335, y, x + i * 335 + w, y + h)
            rounded_box(draw, box, fill)
            centered_multiline(draw, (box[0] + 15, box[1] + 22, box[2] - 15, box[1] + 92), head, font(28, True), "17324D")
            centered_multiline(draw, (box[0] + 15, box[1] + 102, box[2] - 15, box[3] - 20), sub, font(23), "233241")
            if i < len(steps) - 1:
                arrow(draw, (box[2] + 14, y + h // 2), (box[2] + 48, y + h // 2))
        draw.rounded_rectangle((170, 710, 1630, 855), radius=28, fill=rgb("DDF7F3"), outline=rgb("8AA1B4"), width=3)
        centered_multiline(draw, (205, 725, 1595, 840), "設計原則：App / SDK 不直接決定所有權；Account Manager 決定帳號與租戶綁定，Video Cloud 負責 runtime activation。", font(28, True), "17324D")

    return save_image("05_onboarding_flow.png", draw_fn)


def fig_load_targets() -> Path:
    def draw_fn(draw: ImageDraw.ImageDraw, size: tuple[int, int]) -> None:
        draw.text((70, 45), "8 月 Loading Test 目標", font=font(46, True), fill=rgb("17324D"))
        rounded_box(draw, (140, 190, 810, 635), "DDF7F3", "0F766E", 36, 5)
        rounded_box(draw, (990, 190, 1660, 635), "FFF2CC", "F59E0B", 36, 5)
        centered_multiline(draw, (160, 225, 790, 370), "IoT\n50,000 台", font(58, True), "17324D")
        centered_multiline(draw, (1010, 225, 1640, 370), "IoT Video\n5,000 台", font(58, True), "17324D")
        centered_multiline(draw, (190, 405, 760, 590), "大量連線、telemetry、state update、API latency", font(30, True), "233241")
        centered_multiline(draw, (1040, 405, 1610, 590), "video control plane、WebRTC setup、TURN readiness", font(30, True), "233241")
        metrics = "共同量測：成功率、p95 / p99 latency、錯誤分類、資源使用、恢復能力"
        draw.rounded_rectangle((270, 760, 1530, 885), radius=30, fill=rgb("F3F6F8"), outline=rgb("8AA1B4"), width=3)
        centered_multiline(draw, (300, 775, 1500, 870), metrics, font(31, True), "17324D")

    return save_image("06_load_targets.png", draw_fn)


def make_figures() -> dict[str, Path]:
    FIG_DIR.mkdir(parents=True, exist_ok=True)
    return {
        "product_to_kpi": fig_product_to_kpi(),
        "kpi_map": fig_kpi_map(),
        "three_layer": fig_three_layer(),
        "linode": fig_linode(),
        "onboarding": fig_onboarding(),
        "load_targets": fig_load_targets(),
    }


PPTX_PATH = OUT_DIR / "realtek_video_iot_cloud_status_report.pptx"
PPTX_RENDER_DIR = OUT_DIR / "pptx-rendered"
PPTX_LAYOUT_DIR = OUT_DIR / "pptx-layout"
PPTX_WORK_DIR = OUT_DIR / "pptx-work"

SCHEDULE_SNAPSHOT = {
    "current_position": "Load-test preparation",
    "weekly_goal": "Close runner/profile, metrics, thresholds, fleet prerequisites, video profile, and operator runbook.",
    "next_gate": "June validation before July scale rehearsal",
    "risk": "At risk if load-test profile and evidence packaging are not closed before June validation.",
    "judgement": "at risk",
}

SCHEDULE_MILESTONES = [
    {"period": "May 1-10", "label": "Kickoff", "status": "done", "note": "scope / source-of-truth / target"},
    {"period": "May 11-24", "label": "Foundation", "status": "done", "note": "Linode staging + integration"},
    {"period": "May 25-Jun 7", "label": "Load prep", "status": "current", "note": "runner / metrics / runbook"},
    {"period": "Jun 8-30", "label": "Validation", "status": "next", "note": "small-to-medium + bottlenecks"},
    {"period": "Jul 1-31", "label": "Scale rehearsal", "status": "planned", "note": "50k IoT / 5k video dry run"},
    {"period": "Aug 1", "label": "Load test pass", "status": "target", "note": "50k devices + 5k cameras"},
    {"period": "Aug", "label": "Alpha test", "status": "planned", "note": "SDK included"},
    {"period": "Sep", "label": "Beta test", "status": "planned", "note": "SDK + pilot customer"},
    {"period": "After beta", "label": "Public", "status": "planned", "note": "public release path"},
]

VIDEO_MILESTONES = [
    {"period": "Jun", "label": "Foundation", "status": "current", "note": "WebRTC / media / storage path"},
    {"period": "Jul 1-15", "label": "Video profile", "status": "next", "note": "camera mix / viewer behavior"},
    {"period": "Jul 16-31", "label": "5k rehearsal", "status": "planned", "note": "TURN / storage / metrics"},
    {"period": "Aug 1", "label": "5k cameras pass", "status": "target", "note": "same gate as 50k IoT"},
]

LOAD_READINESS = [
    ["Runner / profile", "partial", "safe staging profile, concurrency ladder, run metadata", "load-test owner", "at risk"],
    ["Fleet / video profile", "not verified", "50k device fleet, 5k camera profile, credentials, test-data hygiene", "fleet owner", "at risk"],
    ["Metrics / thresholds", "partial", "success rate, p95/p99, error taxonomy, WebRTC/TURN/storage metrics", "metrics owner", "flat"],
    ["Infra / multi-host", "partial", "multi-host execution, aggregation, resource dashboard", "DevOps/SRE", "flat"],
    ["Broker / DB / video path", "not verified", "broker, DB, TURN, video storage bottleneck visibility and recovery behavior", "service owners", "at risk"],
    ["Report evidence", "partial", "JSON/Markdown/PPT evidence packaging", "report owner", "down"],
]

DECISIONS = [
    ["Load-test owner", "June validation needs one accountable runner", "Scale evidence slips", "Engineering management"],
    ["Metrics threshold", "Pass/fail needs agreed success criteria", "Results become non-actionable", "Product + engineering"],
    ["Backup/restore evidence", "Production-readiness gap remains open", "Cannot claim production-like staging", "DevOps/SRE"],
]

ALPHA_SUPPORT = [
    ["Account / payment ownership", "DNS, Linode billing, credit-card payment, mail/service accounts", "Move from Kevin personal account to company-managed or approved third-party account before alpha.", "Alpha"],
    ["Operation backup", "DNS, Linode console, deploy, health check, runbook, incident contact", "Assign backup operator so staging/alpha is not blocked when Kevin is unavailable.", "Alpha"],
    ["Temporary alpha internal testers", "4-6 real human testers; at least 3-4 developer/firmware/app testers", "Auto test covers quantity; human developers validate SDK onboarding, sample flow, debug/report behavior during alpha.", "Alpha"],
    ["Temporary beta pilot customer", "1-2 pilot customers or partner use cases", "Beta needs external usage feedback before public scope and production boundary are finalized.", "Beta"],
]

POST_ALPHA_COVERAGE = [
    ["Cloud backend / service owner", "1-2 FTE", "Account Manager, Video Cloud, MQTT/shadow, API, release fixes"],
    ["DevOps / SRE / operation", "1 FTE", "production deploy, monitoring, backup/restore, incident response"],
    ["SDK / app / developer support", "1 FTE", "SDK onboarding, sample app, developer issues, alpha/beta feedback"],
    ["QA / automation / load test", "1 FTE", "regression, auto test, loading-test rerun, release gate"],
    ["Security reviewer", "0.2-0.5 FTE", "PKI, threat model, secret handling, security review gate"],
    ["FAE / pilot customer support", "0.5-1 FTE", "beta pilot customer, PoC support, sales feedback loop"],
]

RISKS = [
    ["50,000-device / 5,000-camera fleet readiness", "not verified", "Define fleet prerequisite, video profile, and credential handling", "fleet owner", "flat"],
    ["Broker/database bottleneck unknown", "evidence-needed", "Run staged load ladder and collect resource metrics", "service owners", "new"],
    ["AppVersion=debug in staging", "open", "Use release version for externally reviewed staging", "release owner", "flat"],
    ["Backup/restore evidence incomplete", "open", "Collect product-level evidence bundle", "DevOps/SRE", "flat"],
]

EVIDENCE_INDEX = [
    ["Live endpoint evidence", "materials.md live health checks", "PASS/FAIL/BLOCKED", "runtime availability only"],
    ["Design evidence", "Admin webui design, client mockups, frontend assets", "available", "operation and product visuals"],
    ["Threat model", "cyber_security/analysis/stride-matrix.md", "drafted", "security review progress"],
    ["Load-test report", ".artifacts/status-reports/YYYY-MM-DD/", "not verified", "pending scale runs"],
]

DECK_REQUIRED_TOPICS = [
    "Schedule", "Loading Test", "Cloud Relationship", "Portal Marketing", "WebRTC/storage",
    "MQTT/shadow", "PKI", "Threat Model", "Linode", "Operation Screenshots", "Evidence Appendix",
]


def material_payload(material: dict[str, object]) -> dict[str, str]:
    payload = dict(material)
    payload["path"] = str(payload["path"])
    return payload


def build_report_payload() -> dict[str, object]:
    figures = make_figures()
    health = collect_linode_health()
    billing = collect_linode_billing()
    return {
        "root": str(ROOT),
        "reportDate": REPORT_DATE,
        "snapshotTimeUtc": SNAPSHOT_TIME_UTC,
        "outDir": str(OUT_DIR),
        "figDir": str(FIG_DIR),
        "docxPath": str(DOCX_PATH),
        "pptxPath": str(PPTX_PATH),
        "pptxRenderDir": str(PPTX_RENDER_DIR),
        "pptxLayoutDir": str(PPTX_LAYOUT_DIR),
        "pptxWorkDir": str(PPTX_WORK_DIR),
        "language": REPORT_LANGUAGE,
        "coreMessage": CORE_MESSAGE,
        "currentStatusSummary": CURRENT_STATUS_SUMMARY,
        "scheduleSnapshot": SCHEDULE_SNAPSHOT,
        "scheduleMilestones": SCHEDULE_MILESTONES,
        "videoMilestones": VIDEO_MILESTONES,
        "loadReadiness": LOAD_READINESS,
        "decisions": DECISIONS,
        "alphaSupport": ALPHA_SUPPORT,
        "postAlphaCoverage": POST_ALPHA_COVERAGE,
        "risks": RISKS,
        "evidenceIndex": EVIDENCE_INDEX,
        "deckRequiredTopics": DECK_REQUIRED_TOPICS,
        "designMaterials": [material_payload(m) for m in DESIGN_MATERIALS],
        "portalWeb": {
            "url": PORTAL_WEB_URL,
            "screenshot": str(PORTAL_WEB_SCREENSHOT),
            "fallbackImage": str(PORTAL_WEB_FALLBACK_IMAGE),
            "captureSource": "webtest.mgmeet.io live homepage",
        },
        "linodeHealth": health,
        "linodeBilling": billing,
        "figures": {key: str(value) for key, value in figures.items()},
        "masterAssets": {
            "cover": str(ROOT / "docs/status-reports/master_slide/assets/image1.png"),
            "chapterBackground": str(ROOT / "docs/status-reports/master_slide/assets/image7.jpg"),
            "logo": str(ROOT / "docs/status-reports/master_slide/assets/image2.png"),
            "logoSmall": str(ROOT / "docs/status-reports/master_slide/assets/image6.png"),
            "footerStrip": str(ROOT / "docs/status-reports/master_slide/assets/image13.jpeg"),
            "sidePanel": str(ROOT / "docs/status-reports/master_slide/assets/image12.png"),
            "masterDeck": str(ROOT / "docs/status-reports/master_slide/powerpoint_master.pptx"),
        },
    }


def main() -> None:
    print(json.dumps(build_report_payload(), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
