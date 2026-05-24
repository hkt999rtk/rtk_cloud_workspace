from __future__ import annotations

import json
import math
import shutil
from datetime import UTC, date, datetime
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from PIL import Image, ImageDraw, ImageFont
from docx import Document
from docx.enum.table import WD_TABLE_ALIGNMENT, WD_CELL_VERTICAL_ALIGNMENT
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from docx.shared import Cm, Inches, Pt, RGBColor


ROOT = Path(__file__).resolve().parents[2]
REPORT_DATE = date.today().isoformat()
SNAPSHOT_TIME_UTC = datetime.now(UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")
OUT_DIR = ROOT / ".artifacts" / "status-reports" / REPORT_DATE
FIG_DIR = OUT_DIR / "figures"
DOCX_PATH = OUT_DIR / "realtek_video_iot_cloud_status_report.docx"

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
        "purpose": "主管與客戶可一眼看到 online rate、attention queue、health distribution。",
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
            "重點：Cloud 不是附屬工具，而是讓模組從硬體規格競爭，延伸為完整解決方案競爭。",
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


def set_cell_shading(cell, fill: str) -> None:
    tc_pr = cell._tc.get_or_add_tcPr()
    shd = OxmlElement("w:shd")
    shd.set(qn("w:fill"), fill)
    tc_pr.append(shd)


def set_cell_margins(cell, top=120, start=120, bottom=120, end=120) -> None:
    tc = cell._tc
    tc_pr = tc.get_or_add_tcPr()
    tc_mar = tc_pr.first_child_found_in("w:tcMar")
    if tc_mar is None:
        tc_mar = OxmlElement("w:tcMar")
        tc_pr.append(tc_mar)
    for m, v in [("top", top), ("start", start), ("bottom", bottom), ("end", end)]:
        node = tc_mar.find(qn(f"w:{m}"))
        if node is None:
            node = OxmlElement(f"w:{m}")
            tc_mar.append(node)
        node.set(qn("w:w"), str(v))
        node.set(qn("w:type"), "dxa")


def set_table_width(table, widths_cm: list[float]) -> None:
    total_dxa = sum(int(width * 567) for width in widths_cm)
    tbl_pr = table._tbl.tblPr
    tbl_w = tbl_pr.find(qn("w:tblW"))
    if tbl_w is None:
        tbl_w = OxmlElement("w:tblW")
        tbl_pr.append(tbl_w)
    tbl_w.set(qn("w:type"), "dxa")
    tbl_w.set(qn("w:w"), str(total_dxa))
    grid = table._tbl.tblGrid
    for child in list(grid):
        grid.remove(child)
    for width in widths_cm:
        col = OxmlElement("w:gridCol")
        col.set(qn("w:w"), str(int(width * 567)))
        grid.append(col)
    for row in table.rows:
        for idx, width in enumerate(widths_cm):
            row.cells[idx].width = Cm(width)
            set_cell_margins(row.cells[idx])
            row.cells[idx].vertical_alignment = WD_CELL_VERTICAL_ALIGNMENT.CENTER


def set_run_font(run, size: float | None = None, bold: bool | None = None, color: str | None = None) -> None:
    run.font.name = "Arial Unicode MS"
    run._element.rPr.rFonts.set(qn("w:eastAsia"), "Heiti TC")
    if size is not None:
        run.font.size = Pt(size)
    if bold is not None:
        run.bold = bold
    if color:
        run.font.color.rgb = RGBColor(*rgb(color))


def add_paragraph(doc: Document, text: str = "", style: str | None = None, align=None):
    p = doc.add_paragraph(style=style)
    if text:
        r = p.add_run(text)
        set_run_font(r)
    if align is not None:
        p.alignment = align
    return p


def add_heading(doc: Document, text: str, level: int = 1) -> None:
    p = doc.add_heading(level=level)
    r = p.add_run(text)
    set_run_font(r, bold=True, color="17324D" if level <= 2 else "0F766E")


def add_bullet(doc: Document, text: str, level: int = 0) -> None:
    p = doc.add_paragraph(style="List Bullet" if level == 0 else "List Bullet 2")
    r = p.add_run(text)
    set_run_font(r)


def add_number(doc: Document, text: str) -> None:
    p = doc.add_paragraph(style="List Number")
    r = p.add_run(text)
    set_run_font(r)


def add_callout(doc: Document, title: str, body: str, fill: str = "DDF7F3") -> None:
    table = doc.add_table(rows=1, cols=1)
    table.alignment = WD_TABLE_ALIGNMENT.CENTER
    table.autofit = False
    set_table_width(table, [16.2])
    cell = table.cell(0, 0)
    set_cell_shading(cell, fill)
    p = cell.paragraphs[0]
    r = p.add_run(title)
    set_run_font(r, 11.5, True, "17324D")
    p2 = cell.add_paragraph()
    r2 = p2.add_run(body)
    set_run_font(r2, 10.5, None, "233241")
    doc.add_paragraph()


def add_figure(doc: Document, path: Path, caption: str) -> None:
    p = doc.add_paragraph()
    p.alignment = WD_ALIGN_PARAGRAPH.CENTER
    run = p.add_run()
    run.add_picture(str(path), width=Inches(6.75))
    cap = doc.add_paragraph()
    cap.alignment = WD_ALIGN_PARAGRAPH.CENTER
    r = cap.add_run(caption)
    set_run_font(r, 9.5, None, "5B6773")


def add_existing_figure(doc: Document, material: dict[str, object], index: int) -> None:
    source_path = Path(material["path"])
    if not source_path.exists():
        add_callout(
            doc,
            f"素材缺失：{material['title']}",
            f"找不到來源檔案：{material['source']}。此項目保留在 appendix，待素材補齊後重新產生報告。",
            "FFF2CC",
        )
        return
    copied_path = FIG_DIR / f"{index:02d}_{source_path.stem}{source_path.suffix.lower()}"
    copied_path.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source_path, copied_path)
    add_figure(doc, copied_path, str(material["caption"]))


def add_key_value_table(doc: Document, headers: list[str], rows: list[list[str]], widths: list[float]) -> None:
    table = doc.add_table(rows=1, cols=len(headers))
    table.style = "Table Grid"
    table.alignment = WD_TABLE_ALIGNMENT.CENTER
    table.autofit = False
    hdr = table.rows[0].cells
    for i, h in enumerate(headers):
        set_cell_shading(hdr[i], "17324D")
        p = hdr[i].paragraphs[0]
        p.alignment = WD_ALIGN_PARAGRAPH.CENTER
        r = p.add_run(h)
        set_run_font(r, 9.5, True, "FFFFFF")
    for row in rows:
        cells = table.add_row().cells
        for i, val in enumerate(row):
            if i == 0:
                set_cell_shading(cells[i], "F3F6F8")
            p = cells[i].paragraphs[0]
            p.alignment = WD_ALIGN_PARAGRAPH.LEFT if i != 0 else WD_ALIGN_PARAGRAPH.CENTER
            r = p.add_run(val)
            set_run_font(r, 9.2, i == 0, "233241")
    set_table_width(table, widths)
    doc.add_paragraph()


def configure_section(section) -> None:
    section.page_width = Inches(8.5)
    section.page_height = Inches(11)
    section.top_margin = Inches(0.8)
    section.bottom_margin = Inches(0.7)
    section.left_margin = Inches(0.8)
    section.right_margin = Inches(0.8)
    section.header_distance = Inches(0.35)
    section.footer_distance = Inches(0.35)

    footer = section.footer.paragraphs[0]
    footer.alignment = WD_ALIGN_PARAGRAPH.CENTER
    r = footer.add_run("Realtek Video / IoT Cloud 狀態報告 | Internal Draft")
    set_run_font(r, 8.5, None, "5B6773")


def configure_doc(doc: Document) -> None:
    configure_section(doc.sections[0])
    styles = doc.styles
    normal = styles["Normal"]
    normal.font.name = "Arial Unicode MS"
    normal._element.rPr.rFonts.set(qn("w:eastAsia"), "Heiti TC")
    normal.font.size = Pt(10.5)
    normal.paragraph_format.space_after = Pt(6)
    normal.paragraph_format.line_spacing = 1.12

    for style_name, size, color in [
        ("Title", 24, "17324D"),
        ("Subtitle", 12, "5B6773"),
        ("Heading 1", 17, "17324D"),
        ("Heading 2", 13, "0F766E"),
        ("Heading 3", 11.5, "233241"),
    ]:
        st = styles[style_name]
        st.font.name = "Arial Unicode MS"
        st._element.rPr.rFonts.set(qn("w:eastAsia"), "Heiti TC")
        st.font.size = Pt(size)
        st.font.color.rgb = RGBColor(*rgb(color))
        st.font.bold = style_name.startswith("Heading") or style_name == "Title"
        st.paragraph_format.space_before = Pt(10 if style_name != "Title" else 0)
        st.paragraph_format.space_after = Pt(6)

    for style_name in ["List Bullet", "List Bullet 2", "List Number"]:
        st = styles[style_name]
        st.font.name = "Arial Unicode MS"
        st._element.rPr.rFonts.set(qn("w:eastAsia"), "Heiti TC")
        st.font.size = Pt(10.2)
        st.paragraph_format.space_after = Pt(3)


def new_page(doc: Document) -> None:
    doc.add_page_break()


def build_doc() -> None:
    figures = make_figures()
    linode_health = collect_linode_health()
    doc = Document()
    configure_doc(doc)

    # Cover
    title = doc.add_paragraph(style="Title")
    title.alignment = WD_ALIGN_PARAGRAPH.CENTER
    r = title.add_run("Realtek Video / IoT Cloud\n狀態報告")
    set_run_font(r, 25, True, "17324D")
    sub = doc.add_paragraph(style="Subtitle")
    sub.alignment = WD_ALIGN_PARAGRAPH.CENTER
    r = sub.add_run("AmebaPRO 推廣、Cloud 建置、8 月 Loading Test 與商業 KPI 對齊")
    set_run_font(r, 12.5, None, "5B6773")
    doc.add_paragraph()
    add_callout(
        doc,
        "核心管理訊息",
        "天下武功，唯快不破。現階段不應停留在過度討論，而是先快速建立可運作的 Cloud，讓 AmebaPRO、IoT module、SDK、App、OTA、Video Service 有完整展示與驗證鏈條。工具可以加速建置，但正式商用後的維運、SLA、客戶支援與持續改善，仍需要人力與資源投入。",
        "FFF2CC",
    )
    meta = doc.add_paragraph()
    meta.alignment = WD_ALIGN_PARAGRAPH.CENTER
    r = meta.add_run(f"版本：主管簡報式 Word 初稿｜日期：{REPORT_DATE}｜用途：Internal status review")
    set_run_font(r, 9.5, None, "5B6773")
    add_figure(doc, figures["product_to_kpi"], "圖 1：從 AmebaPRO module 到商業 KPI 的路徑")
    new_page(doc)

    add_heading(doc, "Part 1：主管摘要", 1)
    add_callout(
        doc,
        "一頁結論",
        "這個 Cloud 的目的不是單純展示技術，而是補齊 AmebaPRO 與 IoT module 推廣所需的完整解決方案：module、SDK、App、Cloud onboarding、Video、OTA、Admin 管理與可量測的 Loading Test。8 月的 50,000 IoT / 5,000 IoT Video loading test，應被視為技術驗證與商業信心指標。",
        "DDF7F3",
    )

    add_heading(doc, "1. 為什麼要做這個 Cloud", 2)
    for item in [
        "AmebaPRO 推廣缺少一個可展示、可驗證、可讓客戶快速導入的 Video Cloud。",
        "這不是單純做 server，而是補齊「module + SDK + app + cloud + OTA + video + management」的完整產品路徑。",
        "主管要看到的重點：Cloud 讓模組銷售從硬體規格競爭，延伸到解決方案競爭。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "2. 技術與商業 KPI 的邏輯鏈", 2)
    add_figure(doc, figures["kpi_map"], "圖 2：技術成果如何轉化為商業 KPI")
    add_key_value_table(
        doc,
        ["技術成果", "商業效果", "可觀察 KPI"],
        [
            ["Cloud 可部署", "客戶 PoC 可以快速開始", "PoC 啟動時間、部署成功率"],
            ["Device 可上線", "證明 module 可被 cloud 管理", "active devices、online success rate"],
            ["SDK 可整合", "降低 app / firmware 開發門檻", "SDK adoption、integration time"],
            ["OTA 可管理", "支援售後更新與產品生命週期", "OTA success rate、rollback cases"],
            ["Video 可連線", "支撐 AmebaPRO video value proposition", "video setup success、viewer latency"],
            ["Admin 可觀測", "降低支援與維運盲點", "support effort、incident response time"],
        ],
        [3.2, 6.0, 6.4],
    )

    add_heading(doc, "3. 8 月 Loading Test 目標", 2)
    add_figure(doc, figures["load_targets"], "圖 3：8 月 Loading Test 目標")
    add_paragraph(
        doc,
        "IoT 50,000 與 IoT Video 5,000 不只是技術壓測數字，也是在對內確認資源投入、對外建立客戶信心。測試結果應轉化為可溝通的成功率、延遲、錯誤分類與容量邊界。",
    )

    add_heading(doc, "4. 三層次架構", 2)
    add_figure(doc, figures["three_layer"], "圖 4：Root / Brand Cloud / End User 三層架構")
    add_paragraph(
        doc,
        "Root 是 Realtek Platform 的治理層；Brand Cloud 是客戶或品牌租戶；End User 則在租戶下管理多台 devices。客戶可以選擇自建 private cloud，也可以依附 Realtek-operated cloud；business model 與收費方式仍待定案。",
    )

    add_heading(doc, "5. 目前 Linode Fullset Deployment 現況", 2)
    add_figure(doc, figures["linode"], "圖 5：Linode fullset deployment 的主要服務")
    add_key_value_table(
        doc,
        ["Server / Component", "主要責任"],
        [
            ["Video Cloud", "device runtime、video、WebRTC/TURN、MQTT/EMQX、OTA、telemetry"],
            ["Account Manager", "users、organizations、brand cloud、device registry、provisioning"],
            ["Admin Dashboard", "fleet、provisioning、service health、audit、customer/operator view"],
            ["Frontend / Website", "產品介紹、文件入口、private cloud / commercial messaging"],
            ["PostgreSQL / Object Storage / TLS", "資料持久化、media/firmware artifacts、HTTPS 與 routing"],
        ],
        [4.5, 11.1],
    )

    add_heading(doc, "6. 目前成果與下一步", 2)
    add_key_value_table(
        doc,
        ["已完成 foundation", "下一步"],
        [
            ["Linode fullset deployment", "補齊正式維運、監控、備份還原與 resource owner"],
            ["OTA lifecycle foundation", "用 loading test 與 sample app 驗證實際操作流程"],
            ["Android / iOS SDK 與 reference app 素材", "整理 developer onboarding 與 demo path"],
            ["Pro2 SDK / ASDK artifact delivery", "支援 AmebaPRO firmware / app development"],
            ["Admin / Account / Video Cloud 分工", "將 service health 與 KPI 報表化"],
        ],
        [7.6, 8.0],
    )

    new_page(doc)
    add_heading(doc, "Part 2：Cloud / Product / KPI Detail", 1)

    add_heading(doc, "1. Architecture Detail", 2)
    add_paragraph(
        doc,
        "系統採三層式租戶架構，避免平台治理、客戶品牌雲與終端使用者裝置管理混在一起。這個分層是未來 private cloud、brand cloud 租用、Realtek-operated cloud 三種模式能共存的基礎。",
    )
    for item in [
        "Account Manager 是 identity、tenant、user、device registry 的 source of truth。",
        "Video Cloud 是 video runtime、device transport、OTA、telemetry 的 source of truth。",
        "Admin 是 dashboard / BFF，用於觀測、操作與彙整，不是帳號或裝置資料的最終來源。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "2. 從模組銷售到 Cloud KPI 的端到端鏈條", 2)
    add_key_value_table(
        doc,
        ["階段", "技術內容", "商業連動"],
        [
            ["AmebaPRO module", "硬體能力與 video / IoT foundation", "建立 design-in 的入口"],
            ["SDK / App", "Android、iOS、Native、Go、JS 與 sample flow", "降低客戶開發門檻，縮短 PoC"],
            ["Cloud onboarding", "claim、bind、provisioning、activation", "讓 device 可以被 account / brand cloud 管理"],
            ["Video / OTA / telemetry", "connected product lifecycle", "從硬體銷售延伸為可維運服務"],
            ["Admin / service health", "fleet、health、audit、support view", "降低支援成本，提高客戶信任"],
        ],
        [3.2, 6.2, 6.2],
    )

    add_heading(doc, "3. KPI Framework", 2)
    add_key_value_table(
        doc,
        ["KPI 類型", "建議指標", "用途"],
        [
            ["技術 KPI", "deployment readiness、online success rate、API latency、OTA success、video setup success、load scale", "證明系統能運作、能擴充、能被測量"],
            ["產品 KPI", "SDK integration time、reference app completeness、customer onboarding steps、feature demo coverage", "衡量客戶從拿到 module 到完成 PoC 的難度"],
            ["商業 KPI", "AmebaPRO design-in pipeline、PoC-to-project conversion、brand cloud/customer count、active devices、private deployment opportunities", "連接業務成效與產品投資"],
            ["維運 KPI", "incident response time、backup/restore readiness、monitoring coverage、manual support effort", "判斷正式商用需要多少人力與流程"],
        ],
        [3.0, 8.1, 4.5],
    )

    add_heading(doc, "4. Security / Device Trust", 2)
    add_paragraph(
        doc,
        "PKI/mTLS 是 security detail，不是本報告的商業主標題；但它仍然是 enterprise customer trust 的基礎。主管版可以用一句話理解：合法 device 才能安全上雲，避免未授權連線、錯誤綁定與售後追蹤困難。",
    )
    for item in [
        "Device certificate / mTLS：裝置用憑證證明自己，不靠共用密碼。",
        "Certificate renewal / revocation：支援裝置憑證生命週期與風險處理。",
        "KPI 連動：降低 support risk、提升 enterprise customer trust、支援 private cloud 導入。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "5. API / Cloud Pattern", 2)
    add_paragraph(
        doc,
        "API 設計採用主流 IoT cloud 的共同設計原則，參考 AWS IoT、Azure IoT、GCP IoT 的常見 pattern，但不宣稱與任何一家雲端完全相同。",
    )
    add_key_value_table(
        doc,
        ["Pattern", "本平台對應能力"],
        [
            ["Device identity", "Account Manager registry + Video Cloud runtime identity + device trust"],
            ["Provisioning", "Claim resolve、account bind、cross-service lifecycle command、activation"],
            ["Device state / shadow", "Device state、online/offline、runtime readiness projection"],
            ["Telemetry", "Product telemetry ingestion、admin fleet health、metrics export"],
            ["OTA", "Firmware publish、target、rollout、report、cancel、download"],
            ["RBAC / audit", "Organization roles、platform admin、audit events、service health"],
        ],
        [4.2, 11.4],
    )

    add_heading(doc, "6. Product Features", 2)
    for item in [
        "OTA lifecycle：firmware publish、target、rollout、report、cancel、download。",
        "Burst / load 管理：load profile、concurrency、metrics、p95/p99 latency、error classification。",
        "MQTT / EMQX：IoT device transport。",
        "WebRTC / TURN：video device viewer setup。",
        "Account / brand cloud / user / device registry。",
        "Admin Dashboard：fleet health、provisioning、service health、audit。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "7. SDK 與 Reference App", 2)
    add_paragraph(
        doc,
        "Android SDK、iOS SDK 是客戶與 app team 的主要入口；Native、Go、JavaScript 可作為其他平台與測試整合補充。Reference app / sample flow 用於展示 scan、claim、provision、online、video、OTA 的完整體驗。",
    )
    add_key_value_table(
        doc,
        ["SDK / Sample", "用途"],
        [
            ["Android / iOS SDK", "主要 app integration 入口，支援客戶 mobile app 團隊快速 PoC"],
            ["Native / Go / JavaScript", "設備、工具、server-side validation 與 web sample 補充"],
            ["Reference App / Mockup", "展示 scan / claim / provisioning / status / OTA / video flow"],
            ["Pro2 SDK / ASDK artifacts", "支援 AmebaPRO firmware / app development 與 release handoff"],
        ],
        [5.0, 10.6],
    )

    add_heading(doc, "8. 使用流程圖", 2)
    add_figure(doc, figures["onboarding"], "圖 6：Device onboarding 與 provisioning flow")
    for item in [
        "Brand Cloud 建立，user 被加入 brand cloud 或 customer tenant。",
        "User 使用 app / SDK 掃描 claim token / QR。",
        "Account Manager resolve claim and bind，建立或匹配 registry device。",
        "Account Manager 發出 provisioning intent，cross-service worker 呼叫 Video Cloud。",
        "Video Cloud activation，device 上線並回報 telemetry / video status。",
        "Admin Dashboard 查看狀態、OTA、health、audit。",
    ]:
        add_number(doc, item)

    add_heading(doc, "9. 8 月 Loading Test 計畫", 2)
    add_key_value_table(
        doc,
        ["測試類型", "目標規模", "核心量測"],
        [
            ["IoT Loading Test", "50,000 devices", "大量連線、telemetry、state update、API latency、error rate"],
            ["IoT Video Loading Test", "5,000 video devices", "video control plane、WebRTC setup、TURN readiness、viewer setup latency"],
        ],
        [4.5, 3.5, 7.6],
    )
    add_paragraph(
        doc,
        "測試結果將成為對內資源投入與對外客戶信心的依據。若測試失敗，報告也要能清楚分類瓶頸：API、broker、database、TURN/WebRTC、resource limit、credential/token 或測試資料準備問題。",
    )

    add_heading(doc, "10. Maintain / Operation Reality", 2)
    add_callout(
        doc,
        "維運現實",
        "工具可以快速建立 cloud，但正式商用維運需要人。需要投入 backend/cloud engineer、DevOps/SRE、QA/load test、security/release owner、customer support，並補齊監控告警、備份還原、升級回滾、incident response、SLA 與成本管理。",
        "FFF2CC",
    )

    new_page(doc)
    add_heading(doc, "Part 3：操作畫面與使用流程", 1)
    add_callout(
        doc,
        "操作圖片補充",
        "本節使用 submodule 內既有設計稿，不重新發明 UI。正文只放主管與跨團隊溝通最需要的畫面；完整素材來源列在 appendix，方便後續週報沿用同一套框架。",
        "DCEBFF",
    )

    add_heading(doc, "1. Admin Customer View 操作畫面", 2)
    add_paragraph(
        doc,
        "Admin dashboard 的 Customer View 設計稿已補齊 daily operations 視角：fleet overview、device detail、firmware/OTA、stream health。這些畫面可以直接支撐對外 demo 與內部驗收，而不是只用抽象架構圖說明 cloud capability。",
    )
    for idx, material in enumerate(DESIGN_MATERIALS[:4], start=7):
        add_existing_figure(doc, material, idx)
        add_paragraph(doc, f"操作重點：{material['purpose']}")

    add_heading(doc, "2. SDK / Sample App 操作流程", 2)
    add_existing_figure(doc, DESIGN_MATERIALS[4], 11)
    add_key_value_table(
        doc,
        ["畫面區塊", "操作說明"],
        [
            ["Environment Setup", "設定 base URL、WebSocket URL、Account Manager URL、device id 與 redacted credentials。"],
            ["Add Device / Provision", "登入、選擇 organization、resolve claim token、啟動 provisioning operation，並顯示 readiness evidence。"],
            ["Device Configuration", "讀寫 camera info/config，讓 SDK demo 能展示 device config 操作，而不是只停留在 connection test。"],
            ["Camera Monitor", "保留 16:9 preview frame，顯示 snapshot、WebRTC signaling helper 與 stream request 狀態。"],
            ["Debug Report", "輸出可複製、已 redacted、明確 pass/skip/fail reason 的驗證報告。"],
        ],
        [4.3, 11.3],
    )

    add_heading(doc, "3. Frontend / Product Architecture 素材", 2)
    add_existing_figure(doc, DESIGN_MATERIALS[5], 12)
    add_paragraph(
        doc,
        "Frontend 素材可用於對外產品敘事；Admin 與 SDK mockup 則用於操作證據。後續週報應分清楚：architecture visual 用來說明產品定位，operation screenshot 用來證明 capability 已有可展示介面。",
    )

    new_page(doc)
    add_heading(doc, "Part 4：Linode Staging Deployment & Configuration", 1)
    add_callout(
        doc,
        "目前部署狀態",
        f"本節使用公開 endpoint 的只讀 health check 更新狀態。檢查時間：{SNAPSHOT_TIME_UTC}。這是 status report evidence；正式 sign-off 仍以 product-level evidence bundle 為準。",
        "DDF7F3",
    )
    add_key_value_table(
        doc,
        ["Component", "Public endpoint", "Current runtime shape"],
        [
            ["Video Cloud runtime", "https://video-cloud-staging.realtekconnect.com", "video-cloud-staging-edge；nginx 1.30.x public TLS gateway；proxy to Video Cloud API。"],
            ["Account Manager API", "https://account-manager.video-cloud-staging.realtekconnect.com", "dedicated public VM；nginx TLS；app on 127.0.0.1:18081；local PostgreSQL on 127.0.0.1:5432。"],
            ["Admin dashboard", "https://admin.video-cloud-staging.realtekconnect.com", "dedicated public VM；nginx TLS；Dockerized admin app；local SQLite cache。"],
        ],
        [4.2, 5.8, 5.6],
    )
    add_key_value_table(
        doc,
        ["Configuration boundary", "Non-secret setting / behavior"],
        [
            ["Admin upstreams", "ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-staging.realtekconnect.com；VIDEO_CLOUD_BASE_URL=https://video-cloud-staging.realtekconnect.com"],
            ["TLS / edge", "Public services are reached through domain-based HTTPS. Raw VM IPs and private app ports are not status-report evidence targets."],
            ["Persistence", "Account Manager uses local PostgreSQL; Admin uses local SQLite cache; Video Cloud uses its service-owned runtime storage configuration."],
            ["Secrets", "Linode token、DNS credentials、DB DSN、JWT/auth secrets、MQTT credentials、object storage keys 不寫入報告，只列 configuration category。"],
        ],
        [4.4, 11.2],
    )
    add_key_value_table(
        doc,
        ["Component", "Check", "Result", "Observed"],
        [[row["component"], row["check"], row["result"], row["observed"]] for row in linode_health],
        [3.5, 3.0, 2.0, 7.1],
    )
    add_heading(doc, "Production-ready gaps", 2)
    for item in [
        "Video Cloud staging `/version` 目前仍回報 AppVersion=debug；若要對外稱 release staging，需改成明確 release version。",
        "Public frontend / promotion site 在本 milestone 應明確標示部署完成或 SKIP，不應模糊帶過。",
        "正式 production-like sign-off 仍需要 backup/restore evidence：Account Manager PostgreSQL、Video Cloud PostgreSQL/object storage、Admin SQLite、EMQX 與任何啟用的 broker state。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "Review Checklist", 1)
    for item in [
        "主管摘要可在 5 分鐘內看懂，且能回答：為什麼做、做到哪裡、對業務 KPI 有什麼幫助。",
        "Detail 與 repo 現況一致：Linode deployment、OTA、SDK、MQTT/EMQX、Account/Admin/Video Cloud 分工。",
        "技術與商業鏈條清楚：Cloud 的價值不是展示技術，而是提高 AmebaPRO / module 推廣成功率。",
        "操作圖片能支撐 demo flow：Admin overview/devices/OTA/stream health、SDK setup/provision/config/debug report。",
        "Linode deployment / configuration 只列非敏感資訊，且 health check 以本次產生報告時的只讀檢查為準。",
        "不過度承諾：business model、pricing、SLA、production maintain 都標示為待定或需人力投入。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "Appendix：素材與來源索引", 1)
    add_key_value_table(
        doc,
        ["素材", "來源", "用途"],
        [[str(m["title"]), str(m["source"]), str(m["purpose"])] for m in DESIGN_MATERIALS],
        [3.7, 5.8, 6.1],
    )
    add_heading(doc, "完整素材目錄", 2)
    for item in [
        "repos/rtk_cloud_admin/docs/assets/webui-design/：Customer View dashboard screenshots。",
        "repos/rtk_cloud_client/docs/mockups/：Android/iOS/WebApp sample flow mockups 與 ops lab screenshots。",
        "repos/rtk_cloud_frontend/static/assets/：Connect+ website hero、architecture、feature visuals。",
    ]:
        add_bullet(doc, item)

    add_heading(doc, "Sources / Internal References", 2)
    for item in [
        "rtk_cloud_workspace/docs/private-cloud-deployment.md",
        "rtk_cloud_workspace/docs/linode-staging-deployment-snapshot.md",
        "rtk_cloud_workspace/docs/product-level-evidence.md",
        "rtk_cloud_workspace/docs/business-model.md",
        "rtk_cloud_workspace/docs/account-manager-admin-boundary.md",
        "rtk_cloud_workspace/docs/video-cloud-load-test-roadmap.md",
        "rtk_cloud_admin/docs/webui-customer-view-design.md",
        "rtk_cloud_client/docs/SAMPLE_UI_DESIGN_SYSTEM.md",
        "rtk_cloud_contracts_doc/PRODUCT_ONBOARDING.md",
        "rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md",
        "rtk_cloud_client/docs/SDK_USER_DELIVERY.md",
    ]:
        add_bullet(doc, item)

    DOCX_PATH.parent.mkdir(parents=True, exist_ok=True)
    doc.save(DOCX_PATH)
    print(DOCX_PATH)


if __name__ == "__main__":
    build_doc()
