#!/usr/bin/env node

import fs from "node:fs/promises";
import fsSync from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";

const ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), "../..");
const MODEL_SCRIPT = path.join(ROOT, "tools/status-report/report_model.py");
const SLIDE_SIZE = { width: 1280, height: 720 };
const W = SLIDE_SIZE.width;
const H = SLIDE_SIZE.height;
const FONT_TC = "Microsoft JhengHei";
const FONT_EN = "Arial";

const C = {
  blue: "#4A66AC",
  navy: "#242852",
  lightBlue: "#ACCBF9",
  sky: "#629DD1",
  active: "#297FD5",
  teal: "#5AA2AE",
  gray: "#7F8FA9",
  pale: "#F3F6F8",
  paleBlue: "#EAF3FF",
  paleTeal: "#E9F7F6",
  paleAmber: "#FFF6D8",
  white: "#FFFFFF",
  black: "#111827",
  muted: "#5B6773",
  line: "#C5D2E3",
  green: "#2D8A5E",
  amber: "#B7791F",
  red: "#C2410C",
};

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: ROOT,
    encoding: "utf8",
    ...options,
  });
  if (result.status !== 0) {
    throw new Error(
      [
        `${command} ${args.join(" ")} failed`,
        result.stdout?.trim(),
        result.stderr?.trim(),
      ]
        .filter(Boolean)
        .join("\n"),
    );
  }
  return result.stdout;
}

function loadPayload() {
  const python = process.env.PYTHON || "python3";
  return JSON.parse(run(python, [MODEL_SCRIPT]));
}

function resolveArtifactToolEntrypoint() {
  const require = createRequire(import.meta.url);
  try {
    return require.resolve("@oai/artifact-tool/dist/artifact_tool.mjs");
  } catch {
    const fallback = path.join(
      os.homedir(),
      ".cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/@oai/artifact-tool/dist/artifact_tool.mjs",
    );
    if (fsSync.existsSync(fallback)) return fallback;
    throw new Error("Cannot resolve @oai/artifact-tool. Set NODE_PATH to the bundled runtime node_modules.");
  }
}

async function saveBlobToFile(blob, outputPath) {
  await fs.mkdir(path.dirname(outputPath), { recursive: true });
  await fs.writeFile(outputPath, Buffer.from(await blob.arrayBuffer()));
}

function textStyle({ size = 18, color = C.black, bold = false, face = FONT_TC, align = "left", valign = "top" } = {}) {
  return { size, color, bold, face, align, valign };
}

function addShape(slide, { x, y, w, h, fill = C.white, line = C.line, radius = 0, geometry = "rect", name }) {
  const shape = slide.shapes.add({
    geometry,
    name,
    position: { left: x, top: y, width: w, height: h },
    fill,
    line: line === "none" ? { style: "solid", fill: "#00000000", width: 0 } : { style: "solid", fill: line, width: 1 },
  });
  if (radius && shape.radius !== undefined) shape.radius = radius;
  return shape;
}

function addText(slide, text, frame, style = {}) {
  const s = textStyle(style);
  const shape = addShape(slide, {
    ...frame,
    fill: style.fill || "#00000000",
    line: style.line || "none",
    name: style.name,
  });
  shape.text = text;
  shape.text.fontSize = s.size;
  shape.text.color = s.color;
  shape.text.bold = Boolean(s.bold);
  shape.text.typeface = s.face;
  shape.text.alignment = s.align;
  shape.text.verticalAlignment = s.valign;
  shape.text.insets = style.insets || { left: 8, right: 8, top: 4, bottom: 4 };
  return shape;
}

async function addImage(slide, imagePath, frame, fit = "contain") {
  const blob = await fs.readFile(imagePath);
  const image = slide.images.add({
    blob: blob.buffer.slice(blob.byteOffset, blob.byteOffset + blob.byteLength),
    fit,
    alt: path.basename(imagePath),
  });
  image.position = { left: frame.x, top: frame.y, width: frame.w, height: frame.h };
  return image;
}

async function addBackground(slide, payload, kind = "body") {
  addShape(slide, { x: 0, y: 0, w: W, h: H, fill: C.white, line: "none" });
  if (kind === "cover") {
    await addImage(slide, payload.masterAssets.cover, { x: 0, y: 0, w: W, h: H }, "cover");
    addShape(slide, { x: 0, y: 0, w: W, h: H, fill: "#FFFFFFB8", line: "none" });
    return;
  }
  addShape(slide, { x: 0, y: 682, w: W, h: 8, fill: C.blue, line: "none" });
  addText(slide, "© 2026 Realtek Semiconductor Corp. All rights reserved", { x: 60, y: 690, w: 760, h: 22 }, { size: 10, color: C.gray, face: FONT_EN });
}

async function addHeader(slide, payload, title, kicker = "") {
  await addImage(slide, payload.masterAssets.logoSmall, { x: 56, y: 24, w: 150, h: 31 }, "contain");
  addText(slide, kicker, { x: 56, y: 62, w: 280, h: 20 }, { size: 10, color: C.teal, face: FONT_EN, bold: true });
  addText(slide, title, { x: 56, y: 84, w: 1060, h: 50 }, { size: 27, color: C.navy, bold: true });
  addShape(slide, { x: 56, y: 138, w: 1168, h: 1, fill: C.line, line: "none" });
}

function addPill(slide, text, x, y, w, color = C.blue) {
  addShape(slide, { x, y, w, h: 26, fill: `${color}20`, line: color });
  addText(slide, text, { x: x + 6, y: y + 3, w: w - 12, h: 20 }, { size: 11, color, bold: true, align: "center" });
}

function addTable(slide, headers, rows, frame, widths, options = {}) {
  const rowH = options.rowH || 34;
  const headerH = options.headerH || 34;
  const fontSize = options.fontSize || 12;
  const totalW = widths.reduce((a, b) => a + b, 0);
  let y = frame.y;
  let x = frame.x;
  headers.forEach((h, i) => {
    const w = (widths[i] / totalW) * frame.w;
    addShape(slide, { x, y, w, h: headerH, fill: C.navy, line: C.white });
    addText(slide, h, { x: x + 4, y: y + 6, w: w - 8, h: headerH - 8 }, { size: fontSize, color: C.white, bold: true, align: "center" });
    x += w;
  });
  y += headerH;
  rows.forEach((row, r) => {
    x = frame.x;
    row.forEach((cell, i) => {
      const w = (widths[i] / totalW) * frame.w;
      addShape(slide, { x, y, w, h: rowH, fill: r % 2 ? C.white : C.pale, line: C.line });
      addText(slide, String(cell), { x: x + 5, y: y + 5, w: w - 10, h: rowH - 8 }, { size: fontSize, color: i === 0 ? C.navy : C.black, bold: i === 0, align: i === 0 ? "center" : "left" });
      x += w;
    });
    y += rowH;
  });
}

function addStatusDot(slide, status, x, y) {
  const color = status === "ready" || status === "done" || status === "PASS" ? C.green : status === "blocked" || status === "FAIL" ? C.red : status === "current" || status === "partial" ? C.amber : C.gray;
  addShape(slide, { x, y, w: 12, h: 12, fill: color, line: color, geometry: "ellipse" });
}

function addArrow(slide, x1, y1, x2, y2, color = C.sky) {
  addShape(slide, { x: Math.min(x1, x2), y: y1 - 2, w: Math.abs(x2 - x1), h: 4, fill: color, line: "none" });
  addShape(slide, { x: x2 - 8, y: y2 - 7, w: 14, h: 14, fill: color, line: color, geometry: "triangle" });
}

async function slide01(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 60, w: 220, h: 45 }, "contain");
  addText(slide, "Realtek Video / IoT Cloud\n狀態報告", { x: 72, y: 165, w: 620, h: 120 }, { size: 38, color: C.navy, bold: true });
  addText(slide, "AmebaPRO 推廣、Cloud 建置、Loading Test 與商業 KPI 對齊", { x: 76, y: 295, w: 680, h: 34 }, { size: 18, color: C.muted });
  addShape(slide, { x: 74, y: 366, w: 800, h: 120, fill: "#FFF6D8CC", line: "#E3C25A" });
  addText(slide, "核心管理訊息", { x: 96, y: 382, w: 180, h: 24 }, { size: 14, color: C.navy, bold: true });
  addText(slide, payload.coreMessage, { x: 96, y: 414, w: 750, h: 60 }, { size: 15, color: C.black });
  addText(slide, `日期：${payload.reportDate}｜Snapshot：${payload.snapshotTimeUtc}｜內部狀態審閱`, { x: 76, y: 636, w: 850, h: 26 }, { size: 12, color: C.muted, face: FONT_EN });
  return slide;
}

async function slideMajorTopics(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "本次報告要先建立共同上下文", "MAJOR TOPICS");
  const topics = [
    ["1", "Why this cloud exists", "先說 Realtek platform cloud, brand cloud, device / user 的關係，以及它怎麼支援 module selling。"],
    ["2", "Schedule and release path", "5/1 到 8/1 loading test，再到 Alpha, Beta, Public；每個 gate 要看什麼，現在卡在哪裡。"],
    ["3", "Portal Web and sales loop", "Portal Web 用來看客戶在找什麼，內容要補什麼，demo / PoC lead 有沒有真的進來。"],
    ["4", "Technical and security design", "WebRTC/video storage, MQTT/shadow, PKI, STRIDE, threat model，說清楚設計與目前缺口。"],
    ["5", "Deployment, cost, and support", "Linode runtime, initial cost, payment ownership, operation backup, alpha/beta 支援與 ongoing coverage。"],
  ];
  topics.forEach((t, i) => {
    const x = 110;
    const y = 178 + i * 76;
    addShape(slide, { x, y, w: 1060, h: 56, fill: i % 2 ? C.paleBlue : C.paleTeal, line: C.line });
    addShape(slide, { x: x + 18, y: y + 9, w: 38, h: 38, fill: C.blue, line: C.blue, geometry: "ellipse" });
    addText(slide, t[0], { x: x + 18, y: y + 15, w: 38, h: 22 }, { size: 17, color: C.white, bold: true, align: "center", face: FONT_EN });
    addText(slide, t[1], { x: x + 76, y: y + 10, w: 320, h: 22 }, { size: 15, color: C.navy, bold: true, face: FONT_EN });
    addText(slide, t[2], { x: x + 410, y: y + 9, w: 620, h: 36 }, { size: 12, color: C.black });
  });
  addText(slide, "順序會先建立 business / cloud 結構，再看時程，接著看 portal 和技術安全，最後收斂到成本, 帳號歸屬與人力支援。", { x: 150, y: 610, w: 980, h: 34 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slideOperationalTransition(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 58, w: 205, h: 42 }, "contain");
  addText(slide, "Topic 2", { x: 88, y: 215, w: 300, h: 34 }, { size: 24, color: C.teal, bold: true, face: FONT_EN });
  addText(slide, "Operational Cloud 目前進度與 8 月路徑", { x: 88, y: 270, w: 830, h: 58 }, { size: 36, color: C.navy, bold: true });
  addText(slide, "接下來先看目前狀態、schedule path、loading-test readiness、video gate 與 staging-to-production 架構差距。", { x: 92, y: 365, w: 850, h: 62 }, { size: 18, color: C.black });
  addText(slide, "重點：Aug.1 先完成 50,000 devices + 5,000 video cameras loading test，接著用一個月 alpha 和一個月 beta 把 SDK、pilot customer 與 public path 接起來。", { x: 92, y: 485, w: 900, h: 58 }, { size: 17, color: C.navy, bold: true, fill: "#FFF6D8CC" });
  return slide;
}

async function slide02(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "目前狀態總結", "STATUS SNAPSHOT");
  addTable(slide, ["面向", "目前狀態", "下一步或風險"], payload.currentStatusSummary, { x: 60, y: 170, w: 770, h: 300 }, [1.2, 3.1, 3.1], { rowH: 78, fontSize: 12 });
  addShape(slide, { x: 870, y: 170, w: 330, h: 322, fill: C.paleBlue, line: C.line });
  addText(slide, "Schedule Snapshot", { x: 895, y: 194, w: 280, h: 24 }, { size: 17, color: C.navy, bold: true, face: FONT_EN });
  const items = [
    ["目前位置", payload.scheduleSnapshot.current_position],
    ["本週目標", "Load-test preparation"],
    ["下個 gate", payload.scheduleSnapshot.next_gate],
    ["判定", payload.scheduleSnapshot.judgement],
  ];
  items.forEach((item, i) => {
    addPill(slide, item[0], 896, 244 + i * 58, 92, i === 3 ? C.amber : C.blue);
    addText(slide, item[1], { x: 1004, y: 244 + i * 58, w: 160, h: 34 }, { size: 13, color: C.black, bold: i === 3 });
  });
  return slide;
}

async function slide03(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Cloud 是 module product path", "EXECUTIVE SUMMARY");
  const claims = [
    ["Why", "Cloud 補齊 module + SDK + app + onboarding + OTA + video + admin 的完整產品路徑。"],
    ["Now", "Linode staging、Admin mockups、SDK sample flow、Connect+ architecture 已能支撐端到端展示。"],
    ["Next", "把 demo flow 連到 loading-test evidence，建立 customer PoC 與 commercial KPI 信心。"],
    ["Risk", "Release version、backup/restore、load-test fleet/data、production-like sign-off 仍需補齊。"],
  ];
  claims.forEach((c, i) => {
    addShape(slide, { x: 70 + (i % 2) * 560, y: 175 + Math.floor(i / 2) * 115, w: 500, h: 88, fill: i % 2 ? C.paleBlue : C.paleTeal, line: C.line });
    addText(slide, c[0], { x: 92 + (i % 2) * 560, y: 190 + Math.floor(i / 2) * 115, w: 90, h: 28 }, { size: 19, color: C.blue, bold: true, face: FONT_EN });
    addText(slide, c[1], { x: 172 + (i % 2) * 560, y: 186 + Math.floor(i / 2) * 115, w: 375, h: 58 }, { size: 15, color: C.black });
  });
  await addImage(slide, payload.figures.product_to_kpi, { x: 110, y: 430, w: 1060, h: 190 }, "contain");
  return slide;
}

async function slideWhyCloud(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Why We Need This Cloud", "BUSINESS PURPOSE / FIRST PHASE PRIORITY");
  addText(slide, "Cloud 的第一個任務，是讓 module sales 有一條可展示、可驗證、可導入的產品路徑；先支撐 customer PoC 與 design-in，再逐步擴展到 production operation。", { x: 86, y: 154, w: 1100, h: 44 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

  addText(slide, "Business drivers", { x: 85, y: 225, w: 420, h: 28 }, { size: 22, color: C.navy, bold: true, face: FONT_EN });
  const drivers = [
    ["Module sales path", "客戶能看到 onboarding、SDK/App、OTA、Video、MQTT、Admin operation。"],
    ["Lower PoC friction", "FAE、開發者與客戶 PoC team 有可操作的 cloud capability。"],
    ["Solution value", "module 從硬體規格延伸到可展示、可驗證、可管理的 solution。"],
  ];
  drivers.forEach((item, i) => {
    const y = 275 + i * 86;
    addShape(slide, { x: 75, y, w: 440, h: 62, fill: i % 2 ? C.paleTeal : C.paleBlue, line: C.line });
    addText(slide, item[0], { x: 98, y: y + 10, w: 190, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
    addText(slide, item[1], { x: 300, y: y + 9, w: 185, h: 38 }, { size: 10, color: C.black });
    addArrow(slide, 520, y + 31, 585, y + 31, C.sky);
  });

  addText(slide, "Phase-1 priority chart", { x: 650, y: 225, w: 500, h: 28 }, { size: 22, color: C.navy, bold: true, face: FONT_EN });
  const priorities = [
    ["1", "PoC onboarding path", 330, C.blue, "Account / tenant / device onboarding + SDK/App demo"],
    ["2", "Core runtime services", 270, C.teal, "OTA, MQTT/shadow, WebRTC/video, Admin operations"],
    ["3", "Demo / sales handoff", 215, C.sky, "FAE demo package, portal intro, customer evaluation flow"],
  ];
  priorities.forEach((item, i) => {
    const y = 283 + i * 82;
    addText(slide, item[0], { x: 650, y: y + 8, w: 36, h: 26 }, { size: 19, color: C.navy, bold: true, align: "center", face: FONT_EN });
    addShape(slide, { x: 700, y, w: item[2], h: 42, fill: item[3], line: "none" });
    addText(slide, item[1], { x: 714, y: y + 10, w: item[2] - 20, h: 20 }, { size: 14, color: C.white, bold: true, face: FONT_EN });
    addText(slide, item[4], { x: 700, y: y + 48, w: 390, h: 24 }, { size: 11, color: C.black });
  });
  addText(slide, "Phase 1 success：讓主管、FAE 與客戶能用同一套 cloud path 評估 module solution。", { x: 140, y: 610, w: 1000, h: 34 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slideCustomerUseCaseFit(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Customer / Use Case Fit", "WHO THIS CLOUD HELPS");
  addText(slide, "這頁用來讓主管先判斷：哪些客戶會需要這個 cloud，以及它如何回到 module selling、PoC 和 design-in。", { x: 82, y: 154, w: 1120, h: 40 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

  const rows = payload.customerUseCaseFit || [];
  rows.forEach((row, i) => {
    const x = 70 + (i % 2) * 585;
    const y = 230 + Math.floor(i / 2) * 160;
    const fill = i % 2 ? C.paleTeal : C.paleBlue;
    addShape(slide, { x, y, w: 520, h: 122, fill, line: C.line });
    addText(slide, row[0], { x: x + 22, y: y + 18, w: 215, h: 26 }, { size: 18, color: C.navy, bold: true, face: FONT_EN });
    addShape(slide, { x: x + 260, y: y + 24, w: 205, h: 12, fill: C.blue, line: "none" });
    addText(slide, "need", { x: x + 260, y: y + 39, w: 60, h: 14 }, { size: 8, color: C.muted, face: FONT_EN });
    addText(slide, row[1], { x: x + 22, y: y + 52, w: 235, h: 50 }, { size: 11, color: C.black });
    addText(slide, row[2], { x: x + 280, y: y + 52, w: 205, h: 42 }, { size: 11, color: C.navy, bold: true });
  });

  addShape(slide, { x: 180, y: 575, w: 920, h: 48, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "判斷重點：報告要指出 target customer、使用情境、需要提供的內容，最後連回 IoT module selling / PoC / design-in。", { x: 205, y: 589, w: 870, h: 22 }, { size: 14, color: C.navy, bold: true, align: "center" });
  return slide;
}

async function slide04(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Schedule Path：May 1 到 Public", "SCHEDULE");
  const startX = 78;
  const y = 330;
  const step = 130;
  addShape(slide, { x: startX, y, w: step * (payload.scheduleMilestones.length - 1), h: 6, fill: C.line, line: "none" });
  payload.scheduleMilestones.forEach((m, i) => {
    const x = startX + i * step;
    addStatusDot(slide, m.status, x - 2, y - 8);
    addText(slide, m.period, { x: x - 43, y: 198, w: 112, h: 28 }, { size: 9, color: C.muted, align: "center", face: FONT_EN });
    addText(slide, m.label, { x: x - 48, y: 235, w: 120, h: 46 }, { size: 12, color: m.status === "current" ? C.amber : C.navy, bold: true, align: "center" });
    addText(slide, m.note, { x: x - 52, y: 365, w: 124, h: 58 }, { size: 9, color: C.black, align: "center" });
    if (m.status === "current") {
      addShape(slide, { x: x - 55, y: 178, w: 126, h: 255, fill: "#FFF6D855", line: C.amber });
      addText(slide, "目前位置", { x: x - 38, y: 438, w: 92, h: 22 }, { size: 12, color: C.amber, bold: true, align: "center" });
    }
  });
  addText(slide, "Aug.1 gate: 50,000 IoT devices + 5,000 video cameras loading test; next: Aug alpha with SDK, Sep beta with SDK + pilot customer, then public.", { x: 90, y: 565, w: 1040, h: 42 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });
  return slide;
}

async function slideReleaseGateDefinition(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Release Gate Definition", "AUG.1 / ALPHA / BETA / PUBLIC");
  addText(slide, "每個階段都要有明確通過條件；日期只是排程，gate 要靠 evidence 判斷。", { x: 85, y: 154, w: 1110, h: 36 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

  const gates = payload.releaseGateDefinitions || [];
  const colors = [C.blue, C.teal, C.sky, C.amber];
  gates.forEach((gate, i) => {
    const x = 80 + i * 290;
    const barH = 80 + i * 24;
    addShape(slide, { x, y: 520 - barH, w: 210, h: barH, fill: colors[i], line: "none" });
    addText(slide, gate[0], { x: x - 10, y: 198, w: 230, h: 42 }, { size: 15, color: C.navy, bold: true, align: "center", face: FONT_EN });
    addText(slide, gate[1], { x: x - 5, y: 248, w: 220, h: 44 }, { size: 12, color: C.black, bold: true, align: "center" });
    addText(slide, gate[2], { x: x - 4, y: 540, w: 218, h: 68 }, { size: 9, color: C.black, align: "center" });
    addText(slide, `${i + 1}`, { x: x + 78, y: 520 - barH + 18, w: 54, h: 34 }, { size: 24, color: C.white, bold: true, align: "center", face: FONT_EN });
  });

  addText(slide, "報告更新規則：gate 未過就標 `at risk` / `blocked` / `not verified`，不要用時間進度取代實際驗證。", { x: 140, y: 625, w: 1000, h: 28 }, { size: 14, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slide05(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Loading Test Readiness", "AUG.1: 50,000 DEVICES + 5,000 VIDEO CAMERAS");
  addTable(slide, ["Area", "Status", "Needed before Aug.1", "Owner / dependency", "Risk"], payload.loadReadiness, { x: 62, y: 170, w: 1150, h: 390 }, [1.3, 0.9, 2.9, 1.6, 1], { rowH: 58, fontSize: 11 });
  addText(slide, "Aug.1 loading test 要同時看 IoT device scale 與 video camera path: success rate, p95/p99 latency, error taxonomy, resource use, TURN/storage behavior, recovery.", { x: 90, y: 600, w: 1080, h: 40 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleTeal });
  return slide;
}

async function slide06(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Video Schedule Lane：5,000 cameras by Aug.1", "VIDEO PATH");
  const startX = 145;
  const y = 340;
  addShape(slide, { x: startX, y, w: 900, h: 6, fill: C.line, line: "none" });
  payload.videoMilestones.forEach((m, i) => {
    const x = startX + i * 300;
    addStatusDot(slide, m.status, x, y - 8);
    addShape(slide, { x: x - 95, y: 205, w: 210, h: 95, fill: i === 0 ? C.paleAmber : C.paleBlue, line: C.line });
    addText(slide, m.period, { x: x - 80, y: 218, w: 180, h: 18 }, { size: 10, color: C.muted, face: FONT_EN, align: "center" });
    addText(slide, m.label, { x: x - 80, y: 242, w: 180, h: 26 }, { size: 15, color: C.navy, bold: true, align: "center" });
    addText(slide, m.note, { x: x - 80, y: 405, w: 180, h: 42 }, { size: 12, color: C.black, align: "center" });
  });
  addText(slide, "Video camera loading test 要和 50,000 IoT device gate 同一天收斂: 5,000 cameras 需要 WebRTC setup, TURN behavior, storage path, stream health, metrics 一起看。", { x: 120, y: 555, w: 1000, h: 48 }, { size: 17, color: C.navy, bold: true, align: "center", fill: C.pale });
  return slide;
}

async function slide07(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Cloud Relationship / Tenant Structure", "PLATFORM / BRAND / USER");
  await addImage(slide, payload.figures.three_layer, { x: 60, y: 170, w: 620, h: 390 }, "contain");
  addTable(slide, ["Layer", "Source-of-truth"], [
    ["Account Manager", "identity, tenant, user, organization, registry, provisioning, audit"],
    ["Video Cloud", "runtime activation, device transport, WebRTC/video, MQTT/shadow, OTA/media/logs"],
    ["Admin Console", "dashboard/BFF and evidence aggregator, not authoritative store"],
    ["Frontend", "marketing, documentation/manual portal, lead generation"],
  ], { x: 710, y: 185, w: 500, h: 300 }, [1.2, 2.5], { rowH: 62, fontSize: 11 });
  return slide;
}

async function slideCloudTypes(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Two Cloud Types in This Report", "CONTEXT");
  addText(slide, "這份報告後面會談到兩種不同性質的 cloud；先分清楚，避免把產品營運雲和行銷入口混在一起。", { x: 85, y: 158, w: 1090, h: 34 }, { size: 17, color: C.navy, bold: true, align: "center", fill: C.pale });

  addShape(slide, { x: 80, y: 225, w: 520, h: 315, fill: C.paleBlue, line: C.line });
  addText(slide, "Operational IoT / Video Cloud", { x: 110, y: 252, w: 460, h: 32 }, { size: 22, color: C.navy, bold: true, face: FONT_EN, align: "center" });
  addText(slide, "用途：讓 device、SDK/app、Admin 與 runtime service 可以實際運作。", { x: 118, y: 308, w: 440, h: 32 }, { size: 15, color: C.black });
  addTable(slide, ["Focus", "Examples"], [
    ["Product runtime", "onboarding, provisioning, OTA, MQTT/shadow, WebRTC, media"],
    ["Source of truth", "Account Manager + Video Cloud"],
    ["Evidence", "health, API behavior, screenshots, load-test result"],
    ["Audience", "developers, operators, customer PoC teams"],
  ], { x: 115, y: 360, w: 450, h: 150 }, [1.1, 2.2], { rowH: 30, fontSize: 10 });

  addShape(slide, { x: 680, y: 225, w: 520, h: 315, fill: C.paleTeal, line: C.line });
  addText(slide, "Portal Web / Marketing Cloud", { x: 710, y: 252, w: 460, h: 32 }, { size: 22, color: C.navy, bold: true, face: FONT_EN, align: "center" });
  addText(slide, "用途：讓外部使用者理解產品、閱讀文件、留下 leads，支援銷售轉換。", { x: 718, y: 308, w: 440, h: 32 }, { size: 15, color: C.black });
  addTable(slide, ["Focus", "Examples"], [
    ["Market entry", "SEO, content, docs/manual, CTA, contact form"],
    ["Source of truth", "public website content and lead workflow"],
    ["Evidence", "content readiness, aggregate behavior, lead follow-up"],
    ["Audience", "prospects, sales/product teams, external evaluators"],
  ], { x: 715, y: 360, w: 450, h: 150 }, [1.1, 2.2], { rowH: 30, fontSize: 10 });

  addText(slide, "Operational Cloud 證明產品能運作；Portal Web 證明市場入口與銷售循環能被經營。兩者支援同一個 module business，但成功指標不同。", { x: 125, y: 585, w: 1030, h: 38 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slide08(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Current vs Target Architecture", "STAGING TO PRODUCTION");
  addTable(slide, ["Current staging", "Production Target"], [
    ["Linode public HTTPS staging for demo and evidence", "Production deployment with monitored runtime, runbook, and release/version control"],
    ["Account/Admin/Video source boundaries documented", "Source-of-truth boundaries operated with production ownership and incident response"],
    ["Scaling facility is designed in: service separation, broker/database/storage boundaries, and multi-host direction", "Auto scaling is deployed only in production after load-test evidence defines triggers and scaling units"],
    ["Health endpoints and UI/material evidence exist", "p95/p99, success rate, error taxonomy, recovery behavior, and capacity evidence drive production scaling policy"],
  ], { x: 80, y: 180, w: 1120, h: 350 }, [1, 1], { rowH: 72, fontSize: 13 });
  addText(slide, "Staging 已具備 scaling facility 與 scale-out boundaries 的設計基礎；auto scaling 會放在 Production Target 評估與部署。", { x: 130, y: 585, w: 1020, h: 36 }, { size: 16, color: C.amber, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slidePortalTransition(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 58, w: 205, h: 42 }, "contain");
  addText(slide, "Topic 3", { x: 88, y: 190, w: 300, h: 34 }, { size: 24, color: C.teal, bold: true, face: FONT_EN });
  addText(slide, "接下來切換到另一種 Cloud", { x: 86, y: 245, w: 720, h: 46 }, { size: 34, color: C.navy, bold: true });
  addText(slide, "Portal Web / Digital Marketing", { x: 88, y: 315, w: 720, h: 52 }, { size: 38, color: C.blue, bold: true, face: FONT_EN });
  addText(slide, "接下來看 public website、documentation/manual portal、SEO/content、CTA/lead capture 與 sales follow-up 這一組市場入口。", { x: 92, y: 395, w: 840, h: 78 }, { size: 18, color: C.black });
  addText(slide, "目的：把前面 operational cloud 的產品能力，轉成外部可理解、可搜尋、可聯絡、可追蹤的銷售輔助流程。", { x: 92, y: 485, w: 860, h: 48 }, { size: 18, color: C.navy, bold: true, fill: "#FFF6D8CC" });
  return slide;
}

async function slidePortalIntro(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Portal Web：市場入口與開發者導流", "WEBTEST.MGMEET.IO");
  const portal = payload.portalWeb || {};
  const screenshot = fsSync.existsSync(portal.screenshot || "") ? portal.screenshot : portal.fallbackImage;
  addShape(slide, { x: 58, y: 168, w: 665, h: 382, fill: C.white, line: C.line });
  await addImage(slide, screenshot, { x: 70, y: 180, w: 641, h: 360 }, "contain");
  addText(slide, `Source：${portal.url || "Portal Web live site"}；用途：讓主管與客戶先看到對外產品入口，補足內部 runtime / Admin console 以外的 market-facing view。`, { x: 75, y: 555, w: 640, h: 34 }, { size: 11, color: C.muted, face: FONT_EN });

  addText(slide, "Why we need it", { x: 765, y: 166, w: 390, h: 28 }, { size: 20, color: C.navy, bold: true, face: FONT_EN });
  [
    "支援 Bottom-up Business：讓各單位的 module solution 有可展示、可搜尋、可聯絡的入口。",
    "降低 PoC 起步門檻：把 provisioning、OTA、fleet ops、SDK/App、private cloud option 先講清楚。",
    "把產品興趣轉成 sales follow-up：CTA/contact lead 進入後續業務與技術評估流程。",
  ].forEach((text, i) => {
    const y = 210 + i * 68;
    addStatusDot(slide, "ready", 775, y + 8);
    addText(slide, text, { x: 798, y, w: 380, h: 52 }, { size: 13, color: C.black });
  });

  addTable(slide, ["Feature", "Report value"], [
    ["Product positioning", "Realtek Connect+ / IoT cloud platform message"],
    ["Docs / manual portal", "developer onboarding and customer education"],
    ["Search / language routes", "discoverability for external evaluators"],
    ["CTA / contact", "lead capture and sales handoff"],
    ["SEO / analytics", "content improvement from aggregate behavior"],
  ], { x: 755, y: 425, w: 450, h: 180 }, [1.1, 2.0], { rowH: 28, headerH: 30, fontSize: 10 });
  addText(slide, "此頁介紹 public-facing web：把 cloud capability 轉成市場、開發者與客戶評估時能理解的入口。", { x: 150, y: 626, w: 980, h: 34 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleBlue });
  return slide;
}

async function slide09(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Portal Web / Digital Marketing", "RTK_CLOUD_FRONTEND");
  addText(slide, "Portal Web 要幫我們看清楚：誰在找 solution、他在意哪個功能、最後有沒有走到 demo 或 PoC。", { x: 105, y: 158, w: 1070, h: 42 }, { size: 18, color: C.navy, bold: true, align: "center", fill: C.pale });

  addShape(slide, { x: 415, y: 286, w: 450, h: 118, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Target customer + IoT module sales message", { x: 440, y: 306, w: 400, h: 30 }, { size: 18, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "把市場反應轉成 module positioning, solution story, FAE demo 和 sales follow-up 的調整依據。", { x: 455, y: 348, w: 370, h: 36 }, { size: 13, color: C.black, align: "center" });

  const loopNodes = [
    {
      no: "1",
      title: "Observation",
      body: "看來源, keyword, 停留頁與回訪, 判斷是不是我們要接觸的客戶。",
      x: 92,
      y: 250,
      fill: C.paleBlue,
    },
    {
      no: "2",
      title: "Content Decision",
      body: "補 use case, module capability, cloud service, security, SDK/App demo。",
      x: 92,
      y: 438,
      fill: C.paleTeal,
    },
    {
      no: "3",
      title: "Sales Action",
      body: "用 CTA, demo request, SDK download, PoC request 交給 sales / FAE。",
      x: 878,
      y: 250,
      fill: C.paleTeal,
    },
    {
      no: "4",
      title: "Result / Learning",
      body: "看 lead quality, demo/PoC 回覆和 follow-up, 修正下一輪內容。",
      x: 878,
      y: 438,
      fill: C.paleBlue,
    },
  ];
  loopNodes.forEach((node) => {
    addShape(slide, { x: node.x, y: node.y, w: 310, h: 116, fill: node.fill, line: C.line });
    addShape(slide, { x: node.x + 16, y: node.y + 18, w: 38, h: 38, fill: C.blue, line: C.blue, geometry: "ellipse" });
    addText(slide, node.no, { x: node.x + 22, y: node.y + 24, w: 26, h: 26 }, { size: 16, color: C.white, bold: true, align: "center", face: FONT_EN });
    addText(slide, node.title, { x: node.x + 66, y: node.y + 18, w: 220, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
    addText(slide, node.body, { x: node.x + 66, y: node.y + 48, w: 220, h: 48 }, { size: 12, color: C.black });
  });

  addArrow(slide, 402, 308, 412, 308, C.sky);
  addArrow(slide, 868, 308, 878, 308, C.sky);
  addArrow(slide, 878, 496, 868, 496, C.teal);
  addArrow(slide, 412, 496, 402, 496, C.teal);

  addShape(slide, { x: 210, y: 366, w: 4, h: 72, fill: C.sky, line: "none" });
  addShape(slide, { x: 1035, y: 366, w: 4, h: 72, fill: C.teal, line: "none" });

  addText(slide, "Module sales linkage", { x: 380, y: 578, w: 520, h: 26 }, { size: 19, color: C.navy, bold: true, face: FONT_EN, align: "center" });
  addText(slide, "Portal Web 承接市場反應, 調整內容、demo story、customer PoC path, 最後回到 IoT module selling。", { x: 145, y: 613, w: 990, h: 38 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleBlue });
  return slide;
}

async function slideTechnicalTransition(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 58, w: 205, h: 42 }, "contain");
  addText(slide, "Topic 4", { x: 88, y: 215, w: 300, h: 34 }, { size: 24, color: C.teal, bold: true, face: FONT_EN });
  addText(slide, "Operational Cloud 技術設計與安全管理", { x: 88, y: 270, w: 860, h: 58 }, { size: 36, color: C.navy, bold: true });
  addText(slide, "Portal Web 之後回到 runtime 能力：WebRTC / video storage、MQTT / device shadow、PKI trust lifecycle 與 threat-model review。", { x: 92, y: 365, w: 875, h: 62 }, { size: 18, color: C.black });
  addText(slide, "重點：每一項技術都要對應 capability、control point、evidence 與尚未 sign-off 的缺口。", { x: 92, y: 485, w: 860, h: 58 }, { size: 18, color: C.navy, bold: true, fill: "#FFF6D8CC" });
  return slide;
}

async function slide10(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "WebRTC / Video Storage", "LIVE VIDEO VS STORED MEDIA");
  addShape(slide, { x: 70, y: 175, w: 540, h: 420, fill: C.paleBlue, line: C.line });
  addText(slide, "WebRTC signaling readiness", { x: 95, y: 198, w: 480, h: 28 }, { size: 20, color: C.navy, bold: true });
  ["app offer", "/api/request_webrtc", "owner transport", "device answer", "/answer", "/close"].forEach((s, i) => {
    const y = 250 + i * 50;
    addPill(slide, s, 115, y, 180, i % 2 ? C.teal : C.blue);
    if (i < 5) addArrow(slide, 205, y + 30, 205, y + 45, C.sky);
  });
  addShape(slide, { x: 670, y: 175, w: 540, h: 420, fill: C.paleTeal, line: C.line });
  addText(slide, "Video storage/media readiness", { x: 695, y: 198, w: 480, h: 28 }, { size: 20, color: C.navy, bold: true });
  addTable(slide, ["Capability", "Evidence"], [
    ["Snapshot/media upload", "metadata + clip id"],
    ["List/info/download", "auth + byte range"],
    ["Delete/retention", "not verified"],
    ["Backup/restore", "evidence-needed"],
  ], { x: 700, y: 250, w: 470, h: 240 }, [1.2, 1.4], { rowH: 48, fontSize: 12 });
  return slide;
}

async function slide11(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "MQTT / Device Shadow", "TRANSPORT VS STATE MANAGEMENT");
  addTable(slide, ["MQTT transport", "IoT device shadow"], [
    ["broker/topic connectivity", "cloud-held device state document"],
    ["owner transport, command delivery", "`state.desired`, `state.reported`, `state.delta`"],
    ["events/logs/sample payloads", "`version`, `clientToken`, lifecycle bootstrap"],
    ["not activation/deactivation API", "deactivation + unprovision tombstone governance"],
  ], { x: 90, y: 190, w: 1100, h: 330 }, [1, 1], { rowH: 64, fontSize: 14 });
  addText(slide, "Shadow readiness needs topic/API behavior and state-version evidence; broker health alone is not enough.", { x: 140, y: 575, w: 1000, h: 34 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slideStrideOverview(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "STRIDE：Security implementation 的檢查語言", "THREAT MODEL METHOD");
  addText(slide, "STRIDE 把安全設計拆成六種可檢查風險；報告時用它把 PKI、ACL、audit、revocation、rate limit 等 implementation controls 對到具體威脅。", { x: 82, y: 152, w: 1120, h: 44 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

  const cx = 640;
  const cy = 407;
  const radiusX = 385;
  const radiusY = 154;
  const risks = [
    ["S", "Spoofing", "假冒身份", "cert / mTLS"],
    ["T", "Tampering", "竄改資料", "signed payload"],
    ["R", "Repudiation", "否認行為", "audit trail"],
    ["I", "Information\nDisclosure", "資訊洩漏", "redaction"],
    ["D", "Denial\nof Service", "服務阻斷", "quota / limits"],
    ["E", "Elevation\nof Privilege", "權限提升", "ACL / tenant"],
  ];

  addShape(slide, { x: cx - 155, y: cy - 74, w: 310, h: 148, fill: C.navy, line: C.navy });
  addText(slide, "Security implementation\ncontrol points", { x: cx - 132, y: cy - 48, w: 264, h: 46 }, { size: 20, color: C.white, bold: true, align: "center", face: FONT_EN });
  addText(slide, "PKI identity｜service-options ACL｜audit｜revocation｜rate limit｜evidence scrub", { x: cx - 138, y: cy + 10, w: 276, h: 42 }, { size: 11, color: C.white, align: "center" });

  risks.forEach((r, i) => {
    const angle = (-90 + i * 60) * Math.PI / 180;
    const x = cx + Math.cos(angle) * radiusX;
    const y = cy + Math.sin(angle) * radiusY;
    const cardX = x - 100;
    const cardY = y - 48;
    if (Math.abs(Math.cos(angle)) < 0.1) {
      const y1 = y < cy ? cardY + 96 : cy + 74;
      const y2 = y < cy ? cy - 74 : cardY;
      addShape(slide, { x: cx - 2, y: y1, w: 4, h: Math.max(0, y2 - y1), fill: C.sky, line: "none" });
    } else {
      const leftToRight = x < cx;
      const x1 = leftToRight ? cardX + 200 : cx + 155;
      const x2 = leftToRight ? cx - 155 : cardX;
      addShape(slide, { x: Math.min(x1, x2), y: cardY + 47, w: Math.abs(x2 - x1), h: 4, fill: C.sky, line: "none" });
    }
    addShape(slide, { x: cardX, y: cardY, w: 200, h: 96, fill: i % 2 ? C.paleBlue : C.paleTeal, line: C.line });
    addShape(slide, { x: cardX + 14, y: cardY + 18, w: 44, h: 44, fill: C.blue, line: C.blue, geometry: "ellipse" });
    addText(slide, r[0], { x: cardX + 14, y: cardY + 27, w: 44, h: 22 }, { size: 19, color: C.white, bold: true, align: "center", face: FONT_EN });
    addText(slide, r[1], { x: cardX + 66, y: cardY + 12, w: 118, h: 30 }, { size: 12, color: C.navy, bold: true, face: FONT_EN, align: "center" });
    addText(slide, r[2], { x: cardX + 66, y: cardY + 43, w: 118, h: 20 }, { size: 11, color: C.black, bold: true, align: "center" });
    addText(slide, r[3], { x: cardX + 66, y: cardY + 66, w: 118, h: 18 }, { size: 10, color: C.muted, align: "center", face: FONT_EN });
  });

  addText(slide, "閱讀方式：外圈是 STRIDE 風險類型，中心是我們需要落實並提供 evidence 的安全控制點；下一頁接 PKI/device trust lifecycle。", { x: 115, y: 618, w: 1050, h: 34 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slide12(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Security / PKI Management", "DEVICE TRUST LIFECYCLE");
  const steps = ["factory/MES", "factory enrollment", "certissuer", "device certificate", "mTLS token bootstrap", "service-options ACL", "runtime services"];
  steps.forEach((s, i) => {
    const x = 70 + i * 165;
    addShape(slide, { x, y: 255, w: 125, h: 78, fill: i % 2 ? C.paleBlue : C.paleTeal, line: C.line });
    addText(slide, s, { x: x + 6, y: 278, w: 113, h: 34 }, { size: 12, color: C.navy, bold: true, align: "center" });
    if (i < steps.length - 1) addArrow(slide, x + 128, 294, x + 158, 294, C.sky);
  });
  addTable(slide, ["Management control", "Meaning"], [
    ["identity", "certificate subject, not request-body override"],
    ["entitlement", "`service_options` as canonical ACL"],
    ["audit", "factory enrollment and lifecycle traceability"],
    ["revocation", "security control for future access/token issuance"],
  ], { x: 190, y: 430, w: 900, h: 185 }, [1.1, 2.6], { rowH: 38, fontSize: 13 });
  return slide;
}

async function slideHsmSignerDesign(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "HSM / PKCS#11 Signer Design", "KEY CUSTODY / CERTISSUER / TOKEN SIGNING");
  addText(slide, "新的 signer design 把 signing key custody 從 service file secret 往 HSM / PKCS#11 boundary 移動；service 只拿到 signing capability, 不拿 private key material。", { x: 85, y: 154, w: 1110, h: 44 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

  const lanes = [
    ["Signing key custody", "CA key / JWT key stays in HSM-backed token", "non-exportable key, PIN/module handled as deployment config", C.paleAmber],
    ["PKCS#11 signer adapter", "certissuer signer + RS256 token signer", "select slot/token label/key label, sign only approved digest/CSR", C.paleBlue],
    ["Cloud services", "certissuer, token bootstrap, Account Manager path", "issue cert/token, write audit, fail closed if signer unavailable", C.paleTeal],
  ];
  lanes.forEach((lane, i) => {
    const x = 70 + i * 390;
    addShape(slide, { x, y: 245, w: 320, h: 142, fill: lane[3], line: C.line });
    addText(slide, lane[0], { x: x + 18, y: 268, w: 284, h: 24 }, { size: 18, color: C.navy, bold: true, align: "center", face: FONT_EN });
    addText(slide, lane[1], { x: x + 24, y: 310, w: 272, h: 34 }, { size: 13, color: C.black, bold: true, align: "center" });
    addText(slide, lane[2], { x: x + 24, y: 350, w: 272, h: 26 }, { size: 10, color: C.muted, align: "center" });
    if (i < lanes.length - 1) addArrow(slide, x + 325, 316, x + 376, 316, C.sky);
  });

  addTable(slide, ["Management control", "Report message"], [
    ["Key custody", "Private key should be non-exportable in HSM-backed deployment; report only provider type, not PIN/path/label values."],
    ["Signer boundaries", "Certificate issuance and RS256 token signing use signer adapters; business services do not own raw signing keys."],
    ["Audit and failure mode", "certissuer records request/certificate evidence; signer unavailable must block issuance instead of falling back silently."],
    ["Rollout evidence", "SoftHSM/local CI can prove behavior; production needs HSM provider, key ceremony, backup/rotation and access owner."],
  ], { x: 155, y: 450, w: 970, h: 155 }, [1.15, 3.05], { rowH: 31, headerH: 28, fontSize: 10 });

  addText(slide, "Source：rtk_video_cloud branch `codex/pkcs11-certissuer-token-signers`, certissuer material/signers and auth token signer. No secrets or raw PKCS#11 config should appear in status reports.", { x: 110, y: 625, w: 1060, h: 28 }, { size: 12, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slide13(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Threat Model / Cyber Security Review", "STRIDE PROGRESS");
  addTable(slide, ["Risk theme", "Status", "Next focus"], [
    ["I2 secrets leakage", "evidence-needed", "git/log/artifact/evidence redaction"],
    ["S1/E1 subject-binding confusion", "reviewing", "route auth and legacy credential paths"],
    ["S2 MQTT spoofing", "evidence-needed", "broker auth/TLS/device identity"],
    ["D1 capacity exhaustion", "mitigation-needed", "WebRTC/MQTT/media/DB/TURN limits"],
    ["E2 Admin BFF privilege expansion", "reviewing", "fail-closed upstream/cache behavior"],
  ], { x: 70, y: 178, w: 1120, h: 320 }, [1.4, 0.9, 2.2], { rowH: 54, fontSize: 13 });
  addText(slide, "Health checks prove availability, not security sign-off. Security review must reference trust boundaries, auth, redaction, exposure, and mitigation evidence.", { x: 95, y: 555, w: 1080, h: 48 }, { size: 16, color: C.red, bold: true, align: "center", fill: "#FFF1EB" });
  return slide;
}

async function slideEvidenceTransition(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 58, w: 205, h: 42 }, "contain");
  addText(slide, "Topic 5", { x: 88, y: 215, w: 300, h: 34 }, { size: 24, color: C.teal, bold: true, face: FONT_EN });
  addText(slide, "Deployment、操作流程與 Evidence", { x: 88, y: 270, w: 820, h: 58 }, { size: 36, color: C.navy, bold: true });
  addText(slide, "最後把前面的設計落到目前 Linode staging runtime、health/config boundary、Admin 操作畫面與 SDK sample flow。", { x: 92, y: 365, w: 875, h: 62 }, { size: 18, color: C.black });
  addText(slide, "重點：截圖與 health check 是狀態證據；正式 production readiness 仍需要 release、backup/restore、load-test 與 security sign-off。", { x: 92, y: 485, w: 860, h: 58 }, { size: 18, color: C.navy, bold: true, fill: "#FFF6D8CC" });
  return slide;
}

async function slide14(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Linode Staging Runtime Shape", "DEPLOYMENT TOPOLOGY");
  await addImage(slide, payload.figures.linode, { x: 58, y: 160, w: 690, h: 420 }, "contain");
  addTable(slide, ["Component", "Runtime shape"], [
    ["Video Cloud", "edge nginx TLS gateway, Video Cloud API/runtime"],
    ["Account Manager", "dedicated public VM, nginx, app, local PostgreSQL"],
    ["Admin", "dedicated public VM, nginx, Docker, local SQLite cache"],
    ["Frontend", "marketing/docs/lead-generation portal"],
    ["EMQX/MQTT", "self-hosted broker/service-layer operation"],
  ], { x: 770, y: 178, w: 430, h: 300 }, [1.1, 2.2], { rowH: 51, fontSize: 11 });
  return slide;
}

async function slideCostView(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Initial Operation Cost View", "LINODE BASELINE / AWS PILOT ESTIMATE");
  const billing = payload.linodeBilling || {};
  const aws = payload.awsCostEstimate || {};
  const scenarios = aws.scenarios || {};
  const comparisons = aws.comparisons || {};
  const perUnit = aws.perUnit || {};
  const moneyText = (value) => (value && value !== "n/a" ? `${value}/month` : "n/a");

  addText(slide, "AWS cost discussion should focus on two decisions: whether to pay for CloudHSM key custody, and whether to use the robust redundant design. These are estimates, not AWS actual bills.", { x: 85, y: 154, w: 1110, h: 42 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.pale });

  addShape(slide, { x: 70, y: 220, w: 360, h: 182, fill: C.white, line: C.line });
  addText(slide, "Linode staging baseline", { x: 94, y: 238, w: 300, h: 28 }, { size: 18, color: C.navy, bold: true, face: FONT_EN });
  addShape(slide, { x: 98, y: 284, w: 180, h: 66, fill: C.paleAmber, line: C.line });
  addText(slide, "Current run-rate", { x: 104, y: 292, w: 168, h: 15 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
  addText(slide, billing.estimatedMonthlyRunRate || "n/a", { x: 104, y: 313, w: 168, h: 26 }, { size: 19, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, `Period: ${billing.estimatePeriod || "n/a"}`, { x: 292, y: 285, w: 112, h: 36 }, { size: 8, color: C.black, face: FONT_EN });
  addText(slide, "Baseline only; AWS is separate planning estimate.", { x: 98, y: 366, w: 295, h: 18 }, { size: 10, color: C.navy, bold: true, align: "center", fill: C.paleBlue });

  addShape(slide, { x: 450, y: 220, w: 730, h: 182, fill: C.white, line: C.line });
  addText(slide, `AWS scenario totals (${aws.region || "ap-southeast-1"}, ${aws.currency || "USD"})`, { x: 474, y: 238, w: 680, h: 28 }, { size: 18, color: C.navy, bold: true, face: FONT_EN });
  const totals = [
    ["Base\n(no HSM, no robust)", moneyText(scenarios.baseWithoutCloudHsm), C.paleBlue],
    ["+ CloudHSM\n(default)", moneyText(scenarios.defaultWithOneCloudHsm), C.paleAmber],
    ["Robust\n(no HSM)", moneyText(scenarios.robustWithoutCloudHsm), C.paleTeal],
    ["Robust\n+ 2 HSMs", moneyText(scenarios.robustWithTwoCloudHsms), C.paleAmber],
  ];
  totals.forEach((m, i) => {
    const x = 475 + i * 172;
    addShape(slide, { x, y: 286, w: 154, h: 70, fill: m[2], line: C.line });
    addText(slide, m[0], { x: x + 5, y: 292, w: 144, h: 28 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, m[1], { x: x + 5, y: 326, w: 144, h: 22 }, { size: 13, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });
  addText(slide, `Default unit view: ${perUnit.defaultWithCloudHsmPerUser || "n/a"} / ${perUnit.defaultWithCloudHsmPerDevice || "n/a"}`, { x: 500, y: 370, w: 640, h: 18 }, { size: 11, color: C.navy, bold: true, align: "center", face: FONT_EN, fill: C.pale });

  addShape(slide, { x: 70, y: 430, w: 565, h: 172, fill: C.white, line: C.line });
  addText(slide, "Decision 1: CloudHSM on / off", { x: 94, y: 448, w: 520, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  const cloudHsmRows = [
    ["Default profile", comparisons.cloudHsmDefault?.without, comparisons.cloudHsmDefault?.with, `+${comparisons.cloudHsmDefault?.delta || "n/a"}`],
    ["Robust profile", comparisons.cloudHsmRobust?.without, comparisons.cloudHsmRobust?.with, `+${comparisons.cloudHsmRobust?.delta || "n/a"}`],
  ];
  addTable(slide, ["Profile", "w/o CloudHSM", "w/ CloudHSM", "Difference"], cloudHsmRows, { x: 95, y: 485, w: 510, h: 78 }, [1.2, 1.2, 1.2, 1.1], { rowH: 28, headerH: 24, fontSize: 8.5 });
  addText(slide, "CloudHSM is the largest security-cost decision; robust design uses two HSMs.", { x: 102, y: 575, w: 500, h: 18 }, { size: 10, color: C.navy, bold: true, align: "center", fill: C.paleAmber });

  addShape(slide, { x: 655, y: 430, w: 525, h: 172, fill: C.white, line: C.line });
  addText(slide, "Decision 2: Robust Design on / off", { x: 679, y: 448, w: 480, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  const robustRows = [
    ["No CloudHSM", comparisons.robustWithoutCloudHsm?.without, comparisons.robustWithoutCloudHsm?.with, `+${comparisons.robustWithoutCloudHsm?.delta || "n/a"}`],
    ["With CloudHSM", comparisons.robustWithCloudHsm?.without, comparisons.robustWithCloudHsm?.with, `+${comparisons.robustWithCloudHsm?.delta || "n/a"}`],
  ];
  addTable(slide, ["Security mode", "w/o Robust", "w/ Robust", "Difference"], robustRows, { x: 680, y: 485, w: 470, h: 78 }, [1.1, 1.15, 1.15, 1.05], { rowH: 28, headerH: 24, fontSize: 8.5 });
  addText(slide, "Robust is not a blanket 2x; it adds redundancy to HSM, RDS, cache, NAT, and selected workers.", { x: 682, y: 575, w: 465, h: 18 }, { size: 10, color: C.navy, bold: true, align: "center", fill: C.paleBlue });

  addShape(slide, { x: 70, y: 618, w: 495, h: 48, fill: C.white, line: C.line });
  addText(slide, "Top drivers: CloudHSM, ECS Fargate, RDS PostgreSQL, AWS IoT Core, NAT Gateway.", { x: 92, y: 628, w: 450, h: 16 }, { size: 9.5, color: C.black, face: FONT_EN });
  addText(slide, `Source: docs/cost/aws-pricing-sources.md, collected ${aws.collected || "n/a"}`, { x: 92, y: 646, w: 450, h: 14 }, { size: 8.5, color: C.muted, face: FONT_EN });

  addShape(slide, { x: 585, y: 618, w: 595, h: 48, fill: C.white, line: C.line });
  const caveats = [
    "Estimate excludes tax, support plan, discounts, Savings Plans, Reserved Instances, Marketplace charges, and camera/WebRTC/TURN relay.",
    "Actual AWS bill is not queried; refresh after loading-test evidence.",
  ];
  caveats.forEach((c, i) => {
    addStatusDot(slide, "partial", 600, 628 + i * 18);
    addText(slide, c, { x: 620, y: 624 + i * 18, w: 540, h: 16 }, { size: 8.5, color: C.black, face: FONT_EN });
  });
  return slide;
}

async function slideAwsUnitCost(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Unit Cost Per Month", "PER USER / PER DEVICE VIEW");
  const aws = payload.awsCostEstimate || {};
  const unitCosts = aws.unitCosts || {};
  const basis = unitCosts.basis || {};

  addText(slide, "This page converts the AWS monthly estimate into unit economics. Use raw division for budget sizing; use weighted allocation when explaining the device-heavy business model.", { x: 85, y: 154, w: 1110, h: 42 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.pale });

  const basisItems = [
    ["End users", basis.endUsers || "2,500"],
    ["Registered devices", basis.registeredDevices || "10,000"],
    ["Devices / user", basis.devicesPerUser || "4"],
    ["Allocation", `${basis.weightedUserPool || "10%"} user / ${basis.weightedDevicePool || "90%"} device`],
  ];
  basisItems.forEach((item, i) => {
    const x = 82 + i * 300;
    addShape(slide, { x, y: 218, w: 245, h: 62, fill: i === 3 ? C.paleAmber : C.paleBlue, line: C.line });
    addText(slide, item[0], { x: x + 8, y: 226, w: 229, h: 15 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, item[1], { x: x + 8, y: 248, w: 229, h: 22 }, { size: i === 3 ? 13 : 16, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });

  const rawRows = (unitCosts.rawDivision || []).map((row) => [
    row.scenario,
    row.monthlyTotal,
    row.perUserMonth,
    row.perDeviceMonth,
    row.notes,
  ]);
  addText(slide, "Raw unit cost: total monthly AWS estimate divided by fleet size", { x: 70, y: 315, w: 1120, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Monthly total", "Per user / month", "Per device / month", "Notes"], rawRows, { x: 70, y: 350, w: 1140, h: 150 }, [1.45, 0.85, 0.95, 0.95, 1.45], { rowH: 26, headerH: 26, fontSize: 8.5 });

  const weightedRows = (unitCosts.weightedAllocation || []).map((row) => [
    row.scenario.replace("Default estimate with one CloudHSM", "Default + 1 CloudHSM").replace("Robust redundant design with two CloudHSMs", "Robust + 2 CloudHSMs").replace("Base services only, excluding CloudHSM", "Base services only"),
    row.perUserMonth,
    row.perDeviceMonth,
    row.effectiveUserWithFourDevices,
  ]);
  addText(slide, "Weighted unit cost: 10% user pool / 90% device pool", { x: 70, y: 530, w: 650, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Per user / month", "Per device / month", "1 user + 4 devices"], weightedRows, { x: 70, y: 565, w: 700, h: 104 }, [1.7, 1.0, 1.0, 1.0], { rowH: 24, headerH: 24, fontSize: 8.2 });

  addShape(slide, { x: 810, y: 536, w: 380, h: 118, fill: C.paleTeal, line: C.line });
  addText(slide, "How to present it", { x: 832, y: 555, w: 336, h: 24 }, { size: 17, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "For budget approval, use the monthly total. For customer / business-model discussion, use the weighted device-heavy unit view. Do not add per-user and per-device raw rows together.", { x: 832, y: 590, w: 336, h: 46 }, { size: 10.5, color: C.black, align: "center" });
  return slide;
}

async function slideAwsCostCalculationBase(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Calculation Detail 1/3", "BASE SERVICE LINE ITEMS");
  const aws = payload.awsCostEstimate || {};
  const details = aws.calculationDetails || {};
  const findLine = (name) => (details.baseLineItems || []).find((row) => row.area === name) || {};

  addText(slide, "This page shows how the base monthly AWS estimate is built from service quantities and unit prices. CloudWatch logger and Cognito user-pool cost are included here, but camera/WebRTC/TURN relay remains excluded.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 14.5, color: C.navy, bold: true, align: "center", fill: C.pale });

  const assumptions = [
    ["Region", aws.region || "ap-southeast-1"],
    ["Users / devices", "2,500 users / 10,000 devices"],
    ["Runtime month", "730 hours"],
    ["Currency", aws.currency || "USD"],
  ];
  assumptions.forEach((item, i) => {
    const x = 75 + i * 292;
    addShape(slide, { x, y: 210, w: 250, h: 52, fill: i === 0 ? C.paleTeal : C.paleBlue, line: C.line });
    addText(slide, item[0], { x: x + 8, y: 217, w: 234, h: 14 }, { size: 8.5, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, item[1], { x: x + 8, y: 238, w: 234, h: 16 }, { size: 12, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });

  const baseRows = [
    ["ECS Fargate app services", findLine("ECS Fargate application services").monthlyEstimate || "539.79", "vCPU-hours * 0.05056 + GB-hours * 0.00553; includes account, video, admin, bridges, workers."],
    ["Amazon Cognito User Pools", findLine("Amazon Cognito User Pools").monthlyEstimate || "0.00", "2,500 direct/social MAUs are below the 10,000 MAU free tier; after that use billable MAUs * 0.015."],
    ["RDS PostgreSQL", findLine("RDS PostgreSQL").monthlyEstimate || "182.69", "One shared db.t4g.large: 730 DB-hours * 0.203 + 250 GB storage * 0.138."],
    ["AWS IoT Core", findLine("AWS IoT Core").monthlyEstimate || "164.95", "10,000 connected devices: connection minutes + MQTT messages + shadow ops."],
    ["NAT Gateway", findLine("NAT Gateway").monthlyEstimate || "54.87", "730 gateway-hours * 0.059 + 200 GB processed * 0.059."],
    ["ElastiCache / Valkey", findLine("ElastiCache for Valkey").monthlyEstimate || "28.03", "One cache.t4g.small node * 730 hours * 0.0384."],
    ["Application Load Balancer", findLine("Application Load Balancer").monthlyEstimate || "24.24", "One ALB-hour line plus one LCU-hour assumption."],
    ["CloudWatch Logs", findLine("CloudWatch Logs").monthlyEstimate || "24.53", "33.6 GB log ingestion * 0.70 + 33.6 GB-month retention * 0.03."],
    ["Secrets Manager", findLine("Secrets Manager").monthlyEstimate || "20.05", "50 secrets * 0.40 + 10,000 API calls * 0.000005."],
    ["S3 storage / PUT", findLine("S3 storage and PUT requests").monthlyEstimate || "6.78", "Firmware, backup, release artifact storage; camera snapshots excluded."],
    ["KMS", findLine("KMS").monthlyEstimate || "5.30", "5 customer-managed keys * 1.00 + 100,000 requests * 0.000003."],
    ["Base subtotal", findLine("Base subtotal before HSM/Private CA").monthlyEstimate || "1,063.38", "Sum of base services; excludes CloudHSM, ACM Private CA, support plan, tax, discounts."],
  ];
  addTable(slide, ["Base service item", "USD / month", "Calculation / assumption"], baseRows, { x: 58, y: 285, w: 720, h: 308 }, [1.45, 0.75, 3.15], { rowH: 21, headerH: 23, fontSize: 7.1 });

  const frontendRows = (details.frontendCalculation || []).slice(0, 5).map((row) => [
    row.item.replace("CloudFront ", "CF "),
    row.calculation.replace(" USD", "").replace("requests * ", "req * "),
    row.monthlyEstimate,
  ]);
  addText(slide, "Frontend detail", { x: 810, y: 285, w: 360, h: 20 }, { size: 13, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Item", "Formula", "USD"], frontendRows, { x: 810, y: 312, w: 390, h: 118 }, [1.2, 2.2, 0.55], { rowH: 18, headerH: 20, fontSize: 6.6 });

  const iotRows = (details.iotCalculation || []).slice(0, 5).map((row) => [
    row.item.replace("Telemetry/status", "Telemetry").replace("Downlink command", "Downlink"),
    row.calculation.replace("10,000 devices * ", "10k * ").replace(" USD", ""),
    row.monthlyEstimate,
  ]);
  addText(slide, "AWS IoT Core detail", { x: 810, y: 450, w: 360, h: 20 }, { size: 13, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Item", "Formula", "USD"], iotRows, { x: 810, y: 477, w: 390, h: 118 }, [1.2, 2.2, 0.55], { rowH: 18, headerH: 20, fontSize: 6.6 });

  addShape(slide, { x: 70, y: 620, w: 1130, h: 44, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, `CloudWatch logger included: ${details.cloudWatchFormula || "33.6 GB/month log ingestion plus 30-day retention = 24.53 USD/month."}`, { x: 90, y: 631, w: 1090, h: 18 }, { size: 9.8, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, `Source: docs/cost/aws-pricing-sources.md, collected ${aws.collected || "n/a"}`, { x: 90, y: 650, w: 1090, h: 12 }, { size: 7.5, color: C.muted, align: "center", face: FONT_EN });
  return slide;
}

async function slideAwsCostFormulaBreakdown(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Calculation Detail 2/3", "UNIT PRICE FORMULA BREAKDOWN");
  const aws = payload.awsCostEstimate || {};
  const details = aws.calculationDetails || {};

  addText(slide, "This page expands each major estimate into quantity * public unit price. Example: RDS PostgreSQL is DB instance hours plus storage GB-month; Cognito is 0.00 at pilot size and becomes billable after 10,000 MAUs.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 14.5, color: C.navy, bold: true, align: "center", fill: C.pale });

  const formulaRows = (details.formulaBreakdown || []).map((row) => [
    row.item,
    row.quantity,
    row.unitPrice,
    row.formula,
    row.estimate,
  ]);
  addTable(slide, ["Item", "Quantity", "Public unit price", "Formula", "USD / month"], formulaRows, { x: 42, y: 220, w: 1195, h: 365 }, [1.25, 1.45, 1.55, 2.15, 0.75], { rowH: 23, headerH: 25, fontSize: 6.4 });

  addShape(slide, { x: 66, y: 604, w: 540, h: 58, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "RDS example", { x: 86, y: 613, w: 150, h: 16 }, { size: 12, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "730 DB-hours * 0.203 USD/hour = 148.19; 250 GB-month * 0.138 USD/GB-month = 34.50; total = 182.69 USD/month.", { x: 86, y: 636, w: 500, h: 18 }, { size: 8.8, color: C.black, face: FONT_EN });

  addShape(slide, { x: 650, y: 604, w: 560, h: 58, fill: C.paleBlue, line: C.line });
  addText(slide, "Cognito sensitivity", { x: 670, y: 613, w: 210, h: 16 }, { size: 12, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "Default Essentials formula: max(0, MAUs - 10,000) * 0.015 USD/MAU.", { x: 670, y: 634, w: 520, h: 11 }, { size: 8.7, color: C.black, face: FONT_EN });
  addText(slide, "Examples: 2,500 MAUs = 0.00; 25,000 MAUs = 225.00. Plus 2,500 MAUs = 50.00; SAML/OIDC 2,500 MAUs = 36.75.", { x: 670, y: 648, w: 520, h: 11 }, { size: 8.2, color: C.black, face: FONT_EN });
  return slide;
}

async function slideAwsCostCalculationScenarios(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Calculation Detail 3/3", "SCENARIOS / ROBUST / UNIT COST");
  const aws = payload.awsCostEstimate || {};
  const details = aws.calculationDetails || {};

  addText(slide, "This page explains how the scenario totals are derived from the base subtotal: CloudHSM is a security custody add-on, robust design adds only duplicated infrastructure, and unit cost divides one shared monthly pool.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 14.5, color: C.navy, bold: true, align: "center", fill: C.pale });

  const scenarioRows = (details.scenarioEquations || []).map((row) => [
    row.scenario,
    row.formula,
    row.estimate,
  ]);
  addText(slide, "Scenario equations", { x: 64, y: 218, w: 550, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Formula", "USD / month"], scenarioRows, { x: 58, y: 248, w: 670, h: 130 }, [1.4, 2.55, 0.85], { rowH: 22, headerH: 24, fontSize: 7.2 });

  const robustRows = (details.robustDelta || []).filter((row) => !row.area.includes("Business Support")).map((row) => [
    row.area.replace("ECS Fargate backend services", "ECS Fargate").replace("ElastiCache for Valkey", "Valkey").replace("RDS PostgreSQL", "RDS"),
    row.baseline,
    row.robust,
    row.delta,
  ]);
  addText(slide, "Robust delta is selective, not a blanket 2x", { x: 760, y: 218, w: 440, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Area", "Base", "Robust", "Delta"], robustRows.slice(0, 7), { x: 760, y: 248, w: 450, h: 178 }, [1.35, 0.7, 0.7, 0.7], { rowH: 20, headerH: 22, fontSize: 6.9 });

  const supportRows = (details.supportCalculation || []).filter((row) => row.scenario.includes("Default estimate") || row.scenario.includes("Robust redundant design with two CloudHSMs")).map((row) => [
    row.scenario.replace("Default estimate with one CloudHSM", "Default + 1 HSM").replace("Robust redundant design with two CloudHSMs", "Robust + 2 HSMs"),
    row.calculation.replace("max", "max").replace(" USD/month", ""),
    row.monthlySupportEstimate,
  ]);
  addText(slide, "Support plan is optional in this deck view", { x: 58, y: 414, w: 620, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Business Support+ formula", "Support USD"], supportRows, { x: 58, y: 444, w: 670, h: 70 }, [1.45, 2.45, 0.75], { rowH: 22, headerH: 22, fontSize: 7.0 });
  addText(slide, "Main status slide excludes support, tax, discounts, Savings Plans, Reserved Instances, Marketplace, and camera/WebRTC/TURN. Support can be added as a separate adder when budget owner requests AWS support coverage.", { x: 82, y: 527, w: 620, h: 42 }, { size: 9.5, color: C.black, align: "center", fill: C.paleBlue });

  const unitRows = (aws.unitCosts?.rawDivision || []).map((row) => [
    row.scenario,
    `${row.monthlyTotal} / 2,500 = ${row.perUserMonth}`,
    `${row.monthlyTotal} / 10,000 = ${row.perDeviceMonth}`,
  ]);
  addText(slide, "Unit cost formulas", { x: 760, y: 454, w: 420, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Per user", "Per device"], unitRows, { x: 760, y: 484, w: 450, h: 112 }, [1.45, 1.25, 1.25], { rowH: 20, headerH: 22, fontSize: 6.7 });
  addShape(slide, { x: 760, y: 615, w: 450, h: 48, fill: C.paleTeal, line: C.line });
  addText(slide, "Weighted allocation view: split the same monthly pool 10% to user-driven account/app/admin costs and 90% to device-driven MQTT, shadow, logs, firmware, certificate, storage, and device API workload.", { x: 780, y: 624, w: 410, h: 28 }, { size: 8.8, color: C.navy, bold: true, align: "center" });
  return slide;
}

async function slide15(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Linode Health & Configuration Boundary", "SAFE EVIDENCE");
  addTable(slide, ["Component", "Check", "Result", "Observed"], payload.linodeHealth.map((r) => [r.component, r.check, r.result, r.observed]), { x: 50, y: 170, w: 1180, h: 300 }, [1.4, 0.8, 0.7, 3.4], { rowH: 56, fontSize: 10 });
  addText(slide, "Allowed: domains, non-secret env key names, runtime placement, persistence category, reverse proxy/TLS boundary. Forbidden: DSN, tokens, private keys, bearer tokens, raw customer data.", { x: 90, y: 565, w: 1100, h: 45 }, { size: 14, color: C.navy, bold: true, align: "center", fill: C.pale });
  return slide;
}

async function slide16(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Operation Flow Overview", "DEMO JOURNEY");
  const flow = ["Admin overview", "Abnormal device", "Device drawer", "OTA / stream / telemetry", "SDK sample", "Loading test"];
  flow.forEach((s, i) => {
    const x = 75 + i * 190;
    addShape(slide, { x, y: 285, w: 145, h: 82, fill: i % 2 ? C.paleBlue : C.paleTeal, line: C.line });
    addText(slide, s, { x: x + 8, y: 312, w: 129, h: 28 }, { size: 13, color: C.navy, bold: true, align: "center" });
    if (i < flow.length - 1) addArrow(slide, x + 150, 325, x + 180, 325, C.sky);
  });
  addText(slide, "操作截圖需對應 demo、customer workflow readiness 或 evidence claim，讓畫面和管理結論連在一起。", { x: 150, y: 500, w: 980, h: 40 }, { size: 18, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slide17(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Admin Operation Screenshots", "OPERATION EVIDENCE");
  const mats = payload.designMaterials.slice(0, 4);
  const frames = [
    { x: 62, y: 170, w: 540, h: 205 },
    { x: 682, y: 170, w: 540, h: 205 },
    { x: 62, y: 430, w: 540, h: 205 },
    { x: 682, y: 430, w: 540, h: 205 },
  ];
  for (let i = 0; i < mats.length; i += 1) {
    addShape(slide, { ...frames[i], fill: C.white, line: C.line });
    await addImage(slide, mats[i].path, { x: frames[i].x + 8, y: frames[i].y + 8, w: frames[i].w - 16, h: frames[i].h - 42 }, "contain");
    addText(slide, mats[i].title, { x: frames[i].x + 12, y: frames[i].y + frames[i].h - 30, w: frames[i].w - 24, h: 22 }, { size: 11, color: C.navy, bold: true, align: "center" });
  }
  return slide;
}

async function slide18(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "SDK / Sample App Flow", "CLIENT MOCKUP");
  const mat = payload.designMaterials.find((m) => m.key === "sample_screen_flows");
  await addImage(slide, mat.path, { x: 60, y: 165, w: 650, h: 455 }, "contain");
  addTable(slide, ["Flow", "Evidence purpose"], [
    ["Environment setup", "base URLs, device id, redacted credentials"],
    ["Add device / provision", "claim resolve, org selection, readiness result"],
    ["Device configuration", "camera info/config read/write"],
    ["Camera monitor", "snapshot, WebRTC helper, stream request state"],
    ["Debug report", "redacted pass/skip/fail report"],
  ], { x: 735, y: 180, w: 470, h: 310 }, [1.2, 2.2], { rowH: 51, fontSize: 11 });
  return slide;
}

async function slide19(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Decision / Support Needed", "ALPHA READINESS");
  addText(slide, "Alpha 前要把 Kevin personal-resource dependency 降下來，先補上 company-owned account, market publishing, operation backup, internal tester, beta pilot customer path。", { x: 92, y: 154, w: 1095, h: 42 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.pale });

  const support = payload.alphaSupport || [];
  support.forEach((row, i) => {
    const x = 72 + (i % 2) * 585;
    const y = 215 + Math.floor(i / 2) * 105;
    const fill = i % 2 ? C.paleTeal : C.paleBlue;
    addShape(slide, { x, y, w: 520, h: 92, fill, line: C.line });
    addText(slide, row[0], { x: x + 18, y: y + 10, w: 315, h: 20 }, { size: 13.5, color: C.navy, bold: true, face: FONT_EN });
    addPill(slide, row[3], x + 392, y + 9, 92, row[3] === "Beta" ? C.teal : C.blue);
    addText(slide, row[1], { x: x + 20, y: y + 35, w: 475, h: 20 }, { size: 8.8, color: C.black, bold: true });
    addText(slide, row[2], { x: x + 20, y: y + 58, w: 475, h: 26 }, { size: 8.2, color: C.black });
  });

  const milestones = ["Aug.1 Loading Test", "Alpha", "Beta", "Public"];
  addText(slide, "Milestone impact", { x: 205, y: 565, w: 215, h: 26 }, { size: 18, color: C.navy, bold: true, face: FONT_EN });
  addShape(slide, { x: 470, y: 578, w: 510, h: 4, fill: C.line, line: "none" });
  milestones.forEach((m, i) => {
    const x = 430 + i * 165;
    addShape(slide, { x, y: 560, w: 130, h: 36, fill: i === 1 ? C.paleAmber : C.pale, line: C.line });
    addText(slide, m, { x: x + 6, y: 569, w: 118, h: 18 }, { size: 10, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });
  addText(slide, "Alpha 前先處理帳號歸屬: credit card, Linode billing, DNS/mail service, Google Play Console, Apple Developer Program 都要轉到公司或核准的官方帳號。", { x: 115, y: 625, w: 1050, h: 28 }, { size: 12.5, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slidePostAlphaCoverage(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Ongoing Operation / Development Coverage", "POST-ALPHA BASELINE");
  addText(slide, "Alpha/Beta 的 tester 是短期參與；Public 前後要有人持續負責修問題, 維運, SDK support 和 release gate。", { x: 90, y: 154, w: 1100, h: 42 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

  addTable(slide, ["Area", "Coverage", "Why needed"], payload.postAlphaCoverage || [], { x: 58, y: 225, w: 795, h: 330 }, [1.35, 0.7, 2.3], { rowH: 48, fontSize: 10 });

  addShape(slide, { x: 895, y: 225, w: 300, h: 132, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Ongoing baseline", { x: 920, y: 247, w: 250, h: 26 }, { size: 18, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "Approx. 4.5-6.5 FTE", { x: 920, y: 288, w: 250, h: 26 }, { size: 20, color: C.blue, bold: true, align: "center", face: FONT_EN });
  addText(slide, "equivalent baseline", { x: 920, y: 315, w: 250, h: 20 }, { size: 15, color: C.blue, bold: true, align: "center", face: FONT_EN });
  addText(slide, "including part-time security / FAE", { x: 920, y: 340, w: 250, h: 16 }, { size: 10, color: C.black, align: "center", face: FONT_EN });

  addShape(slide, { x: 895, y: 392, w: 300, h: 118, fill: C.paleBlue, line: C.line });
  addText(slide, "管理重點", { x: 920, y: 413, w: 250, h: 26 }, { size: 17, color: C.navy, bold: true, align: "center" });
  addText(slide, "短期 tester 會回報問題；長期 owner 要負責修正, 上線, 維運, SDK support 和 release 判斷。", { x: 925, y: 452, w: 240, h: 48 }, { size: 11, color: C.black, align: "center" });

  addText(slide, "Public 前至少要有人接住: service fix, operation, SDK support, QA/load test, security review, pilot customer support.", { x: 150, y: 610, w: 980, h: 34 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleTeal });
  return slide;
}

async function slide20(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Appendix：素材與來源索引", "SOURCE INDEX");
  const rows = [
    ...payload.designMaterials.map((m) => [m.title, m.source, "available"]),
    ["Master slide assets", "docs/status-reports/master_slide/assets/", "available"],
    ["Cyber security sources", "cyber_security/README.md + STRIDE matrix", "drafted"],
    ["Deployment evidence", "docs/product-level-evidence.md", "formal sign-off boundary"],
  ];
  addTable(slide, ["Material", "Source", "Status"], rows.slice(0, 11), { x: 55, y: 165, w: 1170, h: 450 }, [1.3, 2.8, 0.9], { rowH: 34, fontSize: 10 });
  return slide;
}

async function slide21(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 456, y: 130, w: 360, h: 74 }, "contain");
  addText(slide, "Review Gate", { x: 390, y: 270, w: 500, h: 54 }, { size: 40, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "Schedule 清楚｜重要數字用 chart｜不放 secrets｜不過度宣稱 production-ready｜Evidence 可追溯", { x: 180, y: 350, w: 920, h: 42 }, { size: 18, color: C.black, bold: true, align: "center" });
  addText(slide, "www.realtek.com", { x: 500, y: 610, w: 280, h: 26 }, { size: 16, color: C.blue, bold: true, align: "center", face: FONT_EN });
  return slide;
}

const SLIDES = [
  slide01, slideMajorTopics, slide07, slideWhyCloud, slideCustomerUseCaseFit, slide03, slideCloudTypes, slideOperationalTransition, slide02, slide04, slideReleaseGateDefinition, slide05, slide06, slide08,
  slidePortalTransition, slidePortalIntro, slide09, slideTechnicalTransition, slide10, slide11, slideStrideOverview, slide12, slideHsmSignerDesign, slide13,
  slideEvidenceTransition, slide14, slideCostView, slideAwsUnitCost, slideAwsCostCalculationBase, slideAwsCostFormulaBreakdown, slideAwsCostCalculationScenarios, slide15, slide16, slide17, slide18, slide19, slidePostAlphaCoverage, slide20, slide21,
];

async function makeContactSheet(previewPaths, outputPath) {
  const script = `
from PIL import Image, ImageDraw
from pathlib import Path
paths = [Path(p) for p in ${JSON.stringify(previewPaths)}]
thumbs = [Image.open(p).convert('RGB').resize((320, 180)) for p in paths]
cols = 3
rows = (len(thumbs) + cols - 1) // cols
sheet = Image.new('RGB', (cols * 340, rows * 220), 'white')
draw = ImageDraw.Draw(sheet)
for i, im in enumerate(thumbs):
    x = (i % cols) * 340 + 10
    y = (i // cols) * 220 + 28
    sheet.paste(im, (x, y))
    draw.text((x, y - 20), f'Slide {i+1:02d}', fill=(36,40,82))
sheet.save(${JSON.stringify(outputPath)})
`;
  run(process.env.PYTHON || "python3", ["-c", script]);
}

async function main() {
  const payload = loadPayload();
  const artifact = await import(pathToFileURL(resolveArtifactToolEntrypoint()).href);
  const { Presentation, PresentationFile } = artifact;
  const presentation = Presentation.create({ slideSize: SLIDE_SIZE });

  for (const addSlide of SLIDES) {
    await addSlide(presentation, payload);
  }

  await fs.mkdir(payload.pptxRenderDir, { recursive: true });
  await fs.mkdir(payload.pptxLayoutDir, { recursive: true });
  const previewPaths = [];
  for (let i = 0; i < presentation.slides.count; i += 1) {
    const slide = presentation.slides.getItem(i);
    const num = String(i + 1).padStart(2, "0");
    const previewPath = path.join(payload.pptxRenderDir, `slide-${num}.png`);
    const preview = await presentation.export({ slide, format: "png", scale: 1 });
    await saveBlobToFile(preview, previewPath);
    previewPaths.push(previewPath);
    try {
      const layout = await presentation.export({ slide, format: "layout" });
      await fs.writeFile(path.join(payload.pptxLayoutDir, `slide-${num}.layout.json`), await layout.text(), "utf8");
    } catch {
      // Layout export is a QA aid; PPTX/PNG output remains the source of truth.
    }
  }

  const pptx = await PresentationFile.exportPptx(presentation);
  await fs.mkdir(path.dirname(payload.pptxPath), { recursive: true });
  await pptx.save(payload.pptxPath);
  const contactSheet = path.join(payload.pptxRenderDir, "contact-sheet.png");
  await makeContactSheet(previewPaths, contactSheet);

  const manifest = {
    pptxPath: payload.pptxPath,
    slideCount: presentation.slides.count,
    previewDir: payload.pptxRenderDir,
    contactSheet,
    requiredTopics: payload.deckRequiredTopics,
  };
  await fs.writeFile(path.join(payload.pptxRenderDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
  console.log(JSON.stringify(manifest, null, 2));
}

main().catch((error) => {
  console.error(error.stack || error.message || String(error));
  process.exit(1);
});
