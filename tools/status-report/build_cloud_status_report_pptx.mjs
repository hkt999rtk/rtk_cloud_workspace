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

const AWS_TOP_COST_ITEMS = [
  "AWS IoT Core",
  "Basic Ingest",
  "IoT Device Management",
  "Business Support",
  "RDS",
  "RDS PostgreSQL",
  "Operational DB",
  "ACM Private CA",
  "hybrid CA",
  "ECS Fargate",
];
const TOP_COST_CELL_STYLE = { color: "#B00020", bold: true };

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

function deviceUnitCostOnly(value, fallback = "0.03 USD/device-month") {
  const match = String(value || "").match(/(\d+(?:\.\d+)?\s+USD\/device-month)/g);
  return match?.at(-1) || fallback;
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
      const cellStyle = options.cellStyle?.(cell, i, row, r) || {};
      addText(slide, String(cell), { x: x + 5, y: y + 5, w: w - 10, h: rowH - 8 }, {
        size: fontSize,
        color: i === 0 ? C.navy : C.black,
        bold: i === 0,
        align: i === 0 ? "center" : "left",
        ...cellStyle,
      });
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
  const dx = x2 - x1;
  const dy = y2 - y1;
  const horizontal = Math.abs(dx) >= Math.abs(dy);
  if (horizontal) {
    addShape(slide, { x: Math.min(x1, x2), y: y1 - 2, w: Math.max(4, Math.abs(dx)), h: 4, fill: color, line: "none" });
  } else {
    addShape(slide, { x: x1 - 2, y: Math.min(y1, y2), w: 4, h: Math.max(4, Math.abs(dy)), fill: color, line: "none" });
  }
  const glyph = horizontal ? (dx >= 0 ? "▶" : "◀") : (dy >= 0 ? "▼" : "▲");
  addText(slide, glyph, { x: x2 - 10, y: y2 - 13, w: 20, h: 24 }, { size: 14, color, bold: true, align: "center", face: FONT_EN });
}

function addFlowBox(slide, title, body, frame, fill = C.paleBlue, options = {}) {
  addShape(slide, { ...frame, fill, line: options.line || C.line });
  addText(slide, title, { x: frame.x + 10, y: frame.y + 10, w: frame.w - 20, h: 18 }, {
    size: options.titleSize || 12,
    color: options.titleColor || C.navy,
    bold: true,
    align: "center",
    face: FONT_EN,
  });
  addText(slide, body, { x: frame.x + 12, y: frame.y + 35, w: frame.w - 24, h: frame.h - 42 }, {
    size: options.bodySize || 8.4,
    color: options.bodyColor || C.black,
    align: "center",
    face: FONT_EN,
  });
}

function addAwsFlowSlide(slide, { intro, steps, sideTitle, sideItems, footer }) {
  addText(slide, intro, { x: 82, y: 152, w: 1120, h: 38 }, { size: 13.5, color: C.navy, bold: true, align: "center", fill: C.pale });
  const startX = 70;
  const gap = 28;
  const stepW = 150;
  const y = 235;
  steps.forEach((step, i) => {
    const x = startX + i * (stepW + gap);
    addFlowBox(slide, step.title, step.body, { x, y, w: stepW, h: 118 }, step.fill || (i % 2 ? C.paleTeal : C.paleBlue), { bodySize: step.bodySize || 8.1 });
    if (i < steps.length - 1) addArrow(slide, x + stepW + 4, y + 59, x + stepW + gap - 6, y + 59, C.sky);
  });

  addShape(slide, { x: 90, y: 392, w: 1095, h: 1, fill: C.line, line: "none" });
  addText(slide, sideTitle, { x: 90, y: 418, w: 520, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  sideItems.forEach((item, i) => {
    const x = 90 + (i % 2) * 555;
    const yy = 462 + Math.floor(i / 2) * 72;
    addShape(slide, { x, y: yy, w: 510, h: 52, fill: i % 2 ? C.paleBlue : C.paleTeal, line: C.line });
    addText(slide, item[0], { x: x + 18, y: yy + 9, w: 165, h: 18 }, { size: 11.5, color: C.navy, bold: true, face: FONT_EN });
    addText(slide, item[1], { x: x + 190, y: yy + 8, w: 290, h: 30 }, { size: 9.2, color: C.black, face: FONT_EN });
  });
  addText(slide, footer, { x: 100, y: 640, w: 1080, h: 18 }, { size: 9, color: C.navy, bold: true, align: "center", fill: C.paleAmber, face: FONT_EN });
}

function addMetricBar(slide, label, value, frame, color, maxValue = 100) {
  addText(slide, label, { x: frame.x, y: frame.y, w: frame.labelW || 120, h: 22 }, { size: 10.5, color: C.navy, bold: true, face: FONT_EN });
  addShape(slide, { x: frame.x + (frame.labelW || 120), y: frame.y + 5, w: frame.w, h: 12, fill: C.pale, line: C.line });
  const filled = Math.max(0, Math.min(frame.w, (Number(value || 0) / maxValue) * frame.w));
  addShape(slide, { x: frame.x + (frame.labelW || 120), y: frame.y + 5, w: filled, h: 12, fill: color, line: "none" });
  addText(slide, `${Number(value || 0).toFixed(1)}%`, { x: frame.x + (frame.labelW || 120) + frame.w + 8, y: frame.y, w: 62, h: 22 }, { size: 10, color: C.black, face: FONT_EN });
}

function addHistoryBars(slide, title, history, frame) {
  addText(slide, title, { x: frame.x, y: frame.y, w: frame.w, h: 20 }, { size: 12.5, color: C.navy, bold: true, face: FONT_EN, align: "center" });
  const plotY = frame.y + 34;
  const plotH = frame.h - 58;
  const plotW = frame.w - 58;
  addShape(slide, { x: frame.x + 28, y: plotY, w: plotW, h: plotH, fill: C.white, line: C.line });
  addText(slide, "100%", { x: frame.x, y: plotY - 2, w: 34, h: 14 }, { size: 7.5, color: C.gray, face: FONT_EN, align: "right" });
  addText(slide, "0", { x: frame.x, y: plotY + plotH - 10, w: 34, h: 14 }, { size: 7.5, color: C.gray, face: FONT_EN, align: "right" });
  const points = Array.isArray(history) ? history : [];
  const usable = points.length ? points : [{ cpuMax: 0, memMax: 0 }];
  const gap = 2;
  const pairW = Math.max(5, Math.floor((plotW - 12) / usable.length) - gap);
  usable.forEach((p, i) => {
    const x = frame.x + 34 + i * (pairW + gap);
    const cpuH = Math.max(1, (Number(p.cpuMax || 0) / 100) * plotH);
    const memH = Math.max(1, (Number(p.memMax || 0) / 100) * plotH);
    const half = Math.max(2, Math.floor(pairW / 2));
    addShape(slide, { x, y: plotY + plotH - cpuH, w: half, h: cpuH, fill: C.sky, line: "none" });
    addShape(slide, { x: x + half, y: plotY + plotH - memH, w: half, h: memH, fill: C.amber, line: "none" });
  });
  addShape(slide, { x: frame.x + frame.w - 128, y: frame.y + frame.h - 16, w: 10, h: 10, fill: C.sky, line: "none" });
  addText(slide, "CPU max", { x: frame.x + frame.w - 114, y: frame.y + frame.h - 20, w: 54, h: 14 }, { size: 7.5, color: C.muted, face: FONT_EN });
  addShape(slide, { x: frame.x + frame.w - 58, y: frame.y + frame.h - 16, w: 10, h: 10, fill: C.amber, line: "none" });
  addText(slide, "Mem max", { x: frame.x + frame.w - 44, y: frame.y + frame.h - 20, w: 54, h: 14 }, { size: 7.5, color: C.muted, face: FONT_EN });
}

function runByLabel(payload, shortLabel) {
  return (payload.loadTestCapacity?.runs || []).find((r) => r.shortLabel === shortLabel) || {};
}

async function slide01(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 60, w: 220, h: 45 }, "contain");
  addText(slide, "Realtek Video / IoT Control Cloud", { x: 72, y: 165, w: 780, h: 62 }, { size: 38, color: C.navy, bold: true });
  addText(slide, "AmebaPRO 推廣、Cloud 建置、Loading Test 與商業 KPI 對齊", { x: 76, y: 295, w: 680, h: 34 }, { size: 18, color: C.muted });
  addShape(slide, { x: 74, y: 366, w: 800, h: 120, fill: "#FFF6D8CC", line: "#E3C25A" });
  addText(slide, payload.coreMessage, { x: 96, y: 392, w: 750, h: 70 }, { size: 15, color: C.black });
  addText(slide, `日期：${payload.reportDate}｜Snapshot：${payload.snapshotTimeUtc}｜內部狀態審閱`, { x: 76, y: 636, w: 850, h: 26 }, { size: 12, color: C.muted, face: FONT_EN });
  return slide;
}

async function slideMajorTopics(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "五個面向", "MAJOR TOPICS");
  const topics = [
    ["1", "Why this cloud exists", "先說 Realtek platform cloud, brand cloud, device / user 的關係，以及它怎麼支援 module selling。"],
    ["2", "Schedule and release path", "5/1 到 8/1 loading test，再到 Alpha, Beta, Public；每個 gate 要看什麼，現在卡在哪裡。"],
    ["3", "Portal Web and sales loop", "Portal Web 用來看客戶在找什麼，內容要補什麼，demo / PoC lead 有沒有真的進來。"],
    ["4", "Technical and security design", "WebRTC/video storage, MQTT/shadow, PKI, STRIDE, threat model，說清楚設計與目前缺口。"],
    ["5", "Deployment, cost, and support", "K8s runtime, initial cost, payment ownership, operation backup, alpha/beta 支援與 ongoing coverage。"],
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
  addText(slide, "簡報順序：先確認 business / cloud 架構，再檢視時程、Portal、技術安全，最後收斂到成本、帳號歸屬與人力支援。", { x: 150, y: 610, w: 980, h: 34 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slideOperationalTransition(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload, "cover");
  await addImage(slide, payload.masterAssets.logo, { x: 72, y: 58, w: 205, h: 42 }, "contain");
  addText(slide, "Topic 2", { x: 88, y: 215, w: 300, h: 34 }, { size: 24, color: C.teal, bold: true, face: FONT_EN });
  addText(slide, "Operational Cloud 目前進度與 8 月路徑", { x: 88, y: 270, w: 830, h: 58 }, { size: 36, color: C.navy, bold: true });
  addText(slide, "接下來先看目前狀態、schedule path、loading-test readiness、video gate 與 staging-to-production 架構差距。", { x: 92, y: 365, w: 850, h: 62 }, { size: 18, color: C.black });
  addText(slide, "重點：Aug.1 先完成 100,000 devices + 5,000 video cameras loading test，接著用一個月 alpha 和一個月 beta 把 SDK、pilot customer 與 public path 接起來。", { x: 92, y: 485, w: 900, h: 58 }, { size: 17, color: C.navy, bold: true, fill: "#FFF6D8CC" });
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
    ["本週目標", payload.scheduleSnapshot.current_week || payload.scheduleSnapshot.weekly_goal || "Load-test preparation"],
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
    ["Now", "Linode/LKE staging、Admin mockups、SDK sample flow、Connect+ architecture 已能支撐端到端展示。"],
    ["Next", "把 100K loading-test evidence 連到 customer PoC 與 commercial KPI，並補 video camera gate。"],
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
  addText(slide, "本頁說明目標客戶與使用情境，並釐清 cloud capability 如何支援 module selling、customer PoC 與 design-in 評估。", { x: 82, y: 154, w: 1120, h: 40 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });

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
  addText(slide, "評估重點：確認 target customer、使用情境與交付內容，並連結到 IoT module selling / PoC / design-in 成果。", { x: 205, y: 589, w: 870, h: 22 }, { size: 14, color: C.navy, bold: true, align: "center" });
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
  addText(slide, "Aug.1 gate: 100,000 IoT devices + 5,000 video cameras loading test; next: Aug alpha with SDK, Sep beta with SDK + pilot customer, then public.", { x: 90, y: 565, w: 1040, h: 42 }, { size: 16, color: C.navy, bold: true, align: "center", fill: C.pale });
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
  await addHeader(slide, payload, "Loading Test Readiness", "IOT 100K VALIDATED / VIDEO GATE PENDING");
  addTable(slide, ["Area", "Status", "Needed before Aug.1", "Owner / dependency", "Risk"], payload.loadReadiness, { x: 62, y: 170, w: 1150, h: 390 }, [1.3, 0.9, 2.9, 1.6, 1], { rowH: 58, fontSize: 11 });
  addText(slide, "IoT 100K 已用 8/8、7/7、6/6 三輪完成容量驗證；下一個 gap 是 5,000 video cameras 的 WebRTC / TURN / storage / bandwidth evidence。", { x: 90, y: 600, w: 1080, h: 40 }, { size: 15, color: C.navy, bold: true, align: "center", fill: C.paleTeal });
  return slide;
}

async function slideLoadTestCapacityResult(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "100K Loading Test Result", "CAPACITY VALIDATION");
  const cap = payload.loadTestCapacity || {};
  const recommended = cap.recommended || {};
  addText(slide, cap.summary || "100K capacity evidence unavailable.", { x: 82, y: 152, w: 1118, h: 42 }, { size: 14.5, color: C.navy, bold: true, align: "center", fill: C.pale });
  addTable(
    slide,
    ["Config", "Result", "MQTT connect", "APP ACK", "CPU p95/max", "Mem p95/max", "Decision"],
    cap.tableRows || [],
    { x: 54, y: 224, w: 1170, h: 150 },
    [0.7, 0.85, 1.35, 1.05, 1.05, 1.05, 1.55],
    { rowH: 30, headerH: 30, fontSize: 8.4 },
  );
  const cards = [
    ["Recommended", recommended.shortLabel || "7/7", "7 nodes + 7 MQTT pods keeps capacity while preserving more memory headroom than 6/6."],
    ["Functional gate", "PASS", "COMPLETE / SUCCESS, 100% connect target, 5,000 desired writes and 5,000 ACKs."],
    ["Correlation gate", "PASS", "Server counters and runtime log streams match the client-side command evidence."],
  ];
  cards.forEach((card, i) => {
    const x = 78 + i * 386;
    addShape(slide, { x, y: 420, w: 335, h: 120, fill: i === 0 ? C.paleAmber : C.paleBlue, line: C.line });
    addText(slide, card[0], { x: x + 18, y: 442, w: 130, h: 20 }, { size: 12, color: C.muted, bold: true, face: FONT_EN });
    addText(slide, card[1], { x: x + 18, y: 472, w: 115, h: 34 }, { size: 24, color: i === 0 ? C.amber : C.green, bold: true, face: FONT_EN });
    addText(slide, card[2], { x: x + 145, y: 452, w: 168, h: 66 }, { size: 9.5, color: C.black });
  });
  addText(slide, "Management readout: 6/6 can pass once, but 7/7 is the safer baseline because capacity decisions must include resource headroom, not only functional success.", { x: 120, y: 602, w: 1040, h: 36 }, { size: 14, color: C.navy, bold: true, align: "center", fill: C.paleTeal });
  return slide;
}

async function slideLoadTestResourceCharts(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Resource Utilization History", "CPU / MEMORY / BANDWIDTH");
  const r7 = runByLabel(payload, "7/7");
  const r6 = runByLabel(payload, "6/6");
  addHistoryBars(slide, "7/7 baseline: node max CPU and memory over test window", r7.k8s?.history || [], { x: 62, y: 168, w: 545, h: 190 });
  addHistoryBars(slide, "6/6 lower bound: node max CPU and memory over test window", r6.k8s?.history || [], { x: 675, y: 168, w: 545, h: 190 });

  const runs = payload.loadTestCapacity?.runs || [];
  addText(slide, "Peak resource comparison by config", { x: 74, y: 395, w: 430, h: 24 }, { size: 15, color: C.navy, bold: true, face: FONT_EN });
  runs.forEach((r, i) => {
    const y = 430 + i * 48;
    const summary = r.k8s?.summary || {};
    addMetricBar(slide, `${r.shortLabel} CPU p95`, Number(summary.cpuP95Max || 0), { x: 80, y, w: 250, labelW: 95 }, C.sky);
    addMetricBar(slide, `${r.shortLabel} Mem p95`, Number(summary.memP95Max || 0), { x: 485, y, w: 250, labelW: 95 }, C.amber);
  });

  addShape(slide, { x: 890, y: 402, w: 300, h: 158, fill: C.pale, line: C.line });
  addText(slide, "Bandwidth evidence", { x: 918, y: 420, w: 245, h: 24 }, { size: 16, color: C.navy, bold: true, face: FONT_EN, align: "center" });
  const bwRows = runs.map((r) => [r.shortLabel, `${Number(r.avgPayloadMbps || 0).toFixed(3)} Mbps`, `${Math.round((r.appPayloadBytes || 0) / 1024 / 1024)} MiB`]);
  addTable(slide, ["Run", "Avg payload", "Bytes"], bwRows, { x: 910, y: 462, w: 260, h: 74 }, [0.65, 1.1, 0.85], { rowH: 18, headerH: 20, fontSize: 6.9 });
  addText(slide, "Current report captures application payload throughput. NIC/link utilization should be added as the next evidence metric for video-camera and TURN tests.", { x: 135, y: 610, w: 1010, h: 34 }, { size: 13.2, color: C.navy, bold: true, align: "center", fill: C.paleAmber });
  return slide;
}

async function slideLoadTestDecisionBasis(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Capacity Decision Basis", "WHY 7/7 IS THE BASELINE");
  const cap = payload.loadTestCapacity || {};
  const coeff = cap.capacityCoefficients || {};
  addText(slide, "Capacity conclusion is model-first but evidence-backed: the formula chooses pods, nodes, and generator VMs, then live runs validate both functional gates and resource headroom.", { x: 84, y: 152, w: 1110, h: 42 }, { size: 14.5, color: C.navy, bold: true, align: "center", fill: C.pale });
  addTable(slide, ["Formula / coefficient", "Value used in report"], [
    ["users", "ceil(devices / 20 devices per user)"],
    ["load_generator_vms", "ceil(devices / 20,000 devices per VM) = 5 for 100K"],
    ["recommended safe devices / MQTT pod", `${Number(coeff.recommendedDevicesPerMqttPod || 0).toLocaleString()} from 7/7 baseline`],
    ["recommended safe devices / node", `${Number(coeff.recommendedDevicesPerNode || 0).toLocaleString()} from 7/7 baseline`],
    ["observed lower-bound pass", `6/6 observed ${Number(coeff.observedMaxDevicesPerMqttPod || 0).toLocaleString()} devices per MQTT pod, but memory p95/max was high`],
  ], { x: 58, y: 225, w: 610, h: 235 }, [1.65, 2.65], { rowH: 40, headerH: 28, fontSize: 10 });

  addTable(slide, ["Decision check", "Pass/fail basis"], (cap.decisionBasis || []).map((x) => {
    const parts = String(x).split(": ");
    return [parts[0], parts.slice(1).join(": ") || x];
  }), { x: 710, y: 225, w: 500, h: 205 }, [1.05, 2.2], { rowH: 38, headerH: 28, fontSize: 9.2 });

  addShape(slide, { x: 95, y: 520, w: 500, h: 75, fill: C.paleTeal, line: C.line });
  addText(slide, "Recommendation", { x: 125, y: 535, w: 160, h: 24 }, { size: 17, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "Use 7 nodes / 7 MQTT pods for the 100K IoT baseline. Treat 6/6 as a lower-bound experiment, not production sizing.", { x: 300, y: 532, w: 260, h: 44 }, { size: 11, color: C.black, bold: true });
  addShape(slide, { x: 685, y: 520, w: 500, h: 75, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Next evidence", { x: 715, y: 535, w: 150, h: 24 }, { size: 17, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "Add NIC bandwidth sampling and run the 5,000 video-camera path with WebRTC/TURN/storage metrics.", { x: 880, y: 532, w: 260, h: 44 }, { size: 11, color: C.black, bold: true });
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
    addShape(slide, { x: x - 95, y: 205, w: 210, h: 95, fill: m.status === "current" ? C.paleAmber : C.paleBlue, line: m.status === "current" ? C.amber : C.line });
    addText(slide, m.period, { x: x - 80, y: 218, w: 180, h: 18 }, { size: 10, color: C.muted, face: FONT_EN, align: "center" });
    addText(slide, m.label, { x: x - 80, y: 242, w: 180, h: 26 }, { size: 15, color: m.status === "current" ? C.amber : C.navy, bold: true, align: "center" });
    if (m.status === "current") {
      addText(slide, "目前位置", { x: x - 80, y: 272, w: 180, h: 16 }, { size: 10, color: C.amber, bold: true, align: "center" });
    }
    addText(slide, m.note, { x: x - 80, y: 405, w: 180, h: 42 }, { size: 12, color: C.black, align: "center" });
  });
  addText(slide, "Video camera loading test 要和 100,000 IoT device gate 同一天收斂: 5,000 cameras 需要 WebRTC setup, TURN behavior, storage path, stream health, metrics 一起看。", { x: 120, y: 555, w: 1000, h: 48 }, { size: 17, color: C.navy, bold: true, align: "center", fill: C.pale });
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
    ["K8s public HTTPS staging for demo and evidence", "Production deployment with monitored runtime, runbook, and release/version control"],
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
  addText(slide, "Portal Web / Digital Marketing", { x: 88, y: 260, w: 720, h: 52 }, { size: 38, color: C.blue, bold: true, face: FONT_EN });
  addText(slide, "public website、documentation/manual portal、SEO/content、CTA/lead capture 與 sales follow-up 這一組市場入口。", { x: 92, y: 350, w: 840, h: 78 }, { size: 18, color: C.black });
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
    ["PKCS#11 signer adapter", "certissuer signer + Ed25519 token signer", "select slot/token label/key label, sign only approved payload/CSR", C.paleBlue],
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
    ["Signer boundaries", "Certificate issuance and Ed25519 token signing use signer adapters; business services do not own raw signing keys."],
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
  addText(slide, "最後把前面的設計落到目前 K8s staging runtime、Admin 操作畫面與 SDK sample flow。", { x: 92, y: 365, w: 875, h: 62 }, { size: 18, color: C.black });
  addText(slide, "重點：截圖與 health check 是狀態證據；正式 production readiness 仍需要 release、backup/restore、load-test 與 security sign-off。", { x: 92, y: 485, w: 860, h: 58 }, { size: 18, color: C.navy, bold: true, fill: "#FFF6D8CC" });
  return slide;
}

async function slide14(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "K8s Staging Runtime Shape", "DEPLOYMENT TOPOLOGY");

  const box = (text, x, y, w, h, fill = C.white, line = C.line, opts = {}) => {
    addShape(slide, { x, y, w, h, fill, line });
    addText(slide, text, { x: x + 8, y: y + 7, w: w - 16, h: h - 12 }, {
      size: opts.size || 10.5,
      color: opts.color || C.navy,
      bold: opts.bold ?? true,
      align: opts.align || "center",
      face: opts.face || FONT_EN,
    });
  };
  const label = (text, x, y, w, color = C.navy) => {
    addText(slide, text, { x, y, w, h: 18 }, { size: 9, color, bold: true, align: "center", face: FONT_EN });
  };
  const hLine = (x1, y, x2, color = C.sky) => {
    addShape(slide, { x: x1, y: y - 2, w: x2 - x1, h: 4, fill: color, line: "none" });
    addText(slide, "▶", { x: x2 - 10, y: y - 13, w: 20, h: 24 }, { size: 14, color, bold: true, align: "center", face: FONT_EN });
  };
  const vLine = (x, y1, y2, color = C.line) => {
    addShape(slide, { x: x - 2, y: y1, w: 4, h: y2 - y1, fill: color, line: "none" });
  };

  addText(slide, "Current staging runs as K8s-only runtime. A thin HAProxy edge forwards public HTTPS and MQTTS to K8s NodePorts; cluster services communicate by Kubernetes service DNS.", { x: 72, y: 150, w: 1136, h: 30 }, { size: 12.5, color: C.navy, bold: true, align: "center", fill: C.pale });

  addShape(slide, { x: 56, y: 212, w: 210, h: 305, fill: C.paleBlue, line: C.line });
  label("Public entry", 78, 228, 166);
  box("External clients\nWeb / API / Devices", 86, 278, 150, 62, C.white, C.line, { size: 10 });
  box("Public DNS\n*.video-cloud-staging\n.realtekconnect.com", 86, 382, 150, 72, C.white, C.line, { size: 9 });
  vLine(161, 340, 382, C.sky);

  addShape(slide, { x: 302, y: 212, w: 210, h: 305, fill: C.paleTeal, line: C.line });
  label("Thin public edge", 324, 228, 166);
  box("HAProxy Edge\nTCP forwarding only\n:443 HTTPS | :8883 MQTTS", 335, 330, 144, 84, C.white, C.line, { size: 9.5 });

  addShape(slide, { x: 548, y: 212, w: 210, h: 305, fill: C.paleAmber, line: "#E3C25A" });
  label("K8s entry points", 570, 228, 166);
  box("NGINX Ingress\nNodePort 30443", 582, 292, 142, 60, C.white, C.line, { size: 9.5 });
  box("MQTT NodePort\n31883", 582, 402, 142, 54, C.white, C.line, { size: 9.5 });

  addShape(slide, { x: 792, y: 212, w: 432, h: 374, fill: C.white, line: C.blue });
  addText(slide, "K8s Cluster: video-cloud-staging | us-sea | 2 nodes", { x: 812, y: 228, w: 392, h: 20 }, { size: 11.5, color: C.navy, bold: true, align: "center", face: FONT_EN });

  addShape(slide, { x: 822, y: 270, w: 370, h: 78, fill: C.paleBlue, line: C.line });
  addText(slide, "Public application routes", { x: 842, y: 280, w: 330, h: 16 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
  addText(slide, "Frontend | Cloud Admin | Account Manager | Video Cloud API | Cloud Logger", { x: 844, y: 312, w: 326, h: 20 }, { size: 10, color: C.navy, bold: true, align: "center", face: FONT_EN });

  addShape(slide, { x: 822, y: 380, w: 370, h: 78, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Video Cloud runtime", { x: 842, y: 390, w: 330, h: 16 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
  addText(slide, "MQTT StatefulSet | Factory Enroll | Cert Issuer | Coturn", { x: 844, y: 422, w: 326, h: 20 }, { size: 10, color: C.navy, bold: true, align: "center", face: FONT_EN });

  addShape(slide, { x: 822, y: 490, w: 175, h: 60, fill: C.paleTeal, line: C.line });
  addText(slide, "Platform services", { x: 838, y: 500, w: 143, h: 16 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
  addText(slide, "PostgreSQL | Redis", { x: 838, y: 526, w: 143, h: 16 }, { size: 10, color: C.navy, bold: true, align: "center", face: FONT_EN });

  addShape(slide, { x: 1018, y: 490, w: 174, h: 60, fill: C.pale, line: C.line });
  addText(slide, "Support services", { x: 1034, y: 500, w: 142, h: 16 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
  addText(slide, "OpenBao | Grafana | Prometheus", { x: 1034, y: 526, w: 142, h: 16 }, { size: 9.3, color: C.navy, bold: true, align: "center", face: FONT_EN });

  hLine(236, 418, 335);
  hLine(479, 372, 582);
  hLine(479, 430, 582);
  hLine(724, 322, 822);
  hLine(724, 430, 822);

  addText(slide, "Main HTTPS path: HAProxy -> NGINX ingress -> public application routes", { x: 84, y: 555, w: 510, h: 22 }, { size: 10, color: C.navy, bold: true, align: "center", fill: C.paleBlue, face: FONT_EN });
  addText(slide, "Main MQTTS path: HAProxy -> MQTT NodePort -> MQTT StatefulSet", { x: 650, y: 555, w: 500, h: 22 }, { size: 10, color: C.navy, bold: true, align: "center", fill: C.paleTeal, face: FONT_EN });
  addText(slide, "Source: cloud_env/staging/lke/env/stack.env, state/video-cloud-staging.state.json, edge-haproxy/upstreams.json. Secrets and kubeconfig contents are redacted.", { x: 74, y: 630, w: 1130, h: 22 }, { size: 9.5, color: C.muted, align: "center", face: FONT_EN });
  return slide;
}

async function slideCostView(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Initial Operation Cost View", "K8S BASELINE / AWS REVIEW-ADJUSTED ESTIMATE");
  const billing = payload.linodeBilling || {};
  const aws = payload.awsCostEstimate || {};
  const adjusted = aws.adjusted || {};
  const scenarios = aws.scenarios || {};
  const adjustedScenarios = adjusted.scenarios || {};
  const adjustedPerUnit = adjusted.perUnit || {};
  const moneyText = (value) => (value && value !== "n/a" ? `${value}/month` : "n/a");

  addText(slide, "AWS review-adjusted model keeps source pricing unchanged, then applies architecture corrections: no always-on CloudHSM, no Managed Integrations, Basic Ingest for telemetry, and operational DB instead of telemetry-in-RDS.", { x: 85, y: 154, w: 1110, h: 42 }, { size: 13.8, color: C.navy, bold: true, align: "center", fill: C.pale });

  addShape(slide, { x: 70, y: 220, w: 360, h: 182, fill: C.white, line: C.line });
  addText(slide, "K8S Staging Baseline", { x: 94, y: 238, w: 300, h: 28 }, { size: 18, color: C.navy, bold: true, face: FONT_EN });
  addShape(slide, { x: 98, y: 284, w: 180, h: 66, fill: C.paleAmber, line: C.line });
  addText(slide, "Current run-rate", { x: 104, y: 292, w: 168, h: 15 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
  addText(slide, billing.estimatedMonthlyRunRate || "n/a", { x: 104, y: 313, w: 168, h: 26 }, { size: 19, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, `Period: ${billing.estimatePeriod || "n/a"}`, { x: 292, y: 285, w: 112, h: 36 }, { size: 8, color: C.black, face: FONT_EN });
  addText(slide, "Baseline only; AWS is separate planning estimate.", { x: 98, y: 366, w: 295, h: 18 }, { size: 10, color: C.navy, bold: true, align: "center", fill: C.paleBlue });

  addShape(slide, { x: 450, y: 220, w: 730, h: 182, fill: C.white, line: C.line });
  addText(slide, `AWS revised estimate (${aws.region || "ap-southeast-1"}, ${aws.currency || "USD"})`, { x: 474, y: 238, w: 680, h: 28 }, { size: 18, color: C.navy, bold: true, face: FONT_EN });
  const totals = [
    ["Original base\n(source snapshot)", moneyText(scenarios.baseWithoutCloudHsm), C.paleBlue],
    ["Revised infra\n(no CA/support)", moneyText(adjustedScenarios.infraBase), C.paleTeal],
    ["Default + CA\n(no support)", moneyText(adjustedScenarios.defaultWithCa), C.paleAmber],
    ["Budget headline\nCA + Support+", moneyText(adjustedScenarios.budgetHeadline), "#FFF1F1"],
  ];
  totals.forEach((m, i) => {
    const x = 475 + i * 172;
    addShape(slide, { x, y: 286, w: 154, h: 70, fill: m[2], line: C.line });
    addText(slide, m[0], { x: x + 5, y: 292, w: 144, h: 28 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, m[1], { x: x + 5, y: 326, w: 144, h: 22 }, { size: 13, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });
  addText(slide, `Budget unit view: ${adjustedPerUnit.budgetPerUser || "n/a"} / ${adjustedPerUnit.budgetPerDevice || "n/a"} (includes Business Support+)`, { x: 500, y: 370, w: 640, h: 18 }, { size: 11, color: "#B00020", bold: true, align: "center", face: FONT_EN, fill: "#FFF1F1" });

  addShape(slide, { x: 70, y: 430, w: 565, h: 172, fill: C.white, line: C.line });
  addText(slide, "AWS review adjustments adopted", { x: 94, y: 448, w: 520, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  const adjustmentRows = (adjusted.adjustments || []).slice(0, 5).map((row) => [
    row.item.replace("AWS ", "").replace(" / CA signing", ""),
    row.original,
    row.revised,
    row.delta,
  ]);
  addTable(slide, ["Item", "Original", "Revised", "Delta"], adjustmentRows, { x: 95, y: 482, w: 510, h: 104 }, [1.55, 0.8, 0.8, 0.8], {
    rowH: 16,
    headerH: 20,
    fontSize: 6.2,
    cellStyle: (_cell, col) => col >= 2 ? TOP_COST_CELL_STYLE : {},
  });
  addText(slide, "Source pricing snapshot is preserved; these are derived AWS-review corrections.", { x: 102, y: 592, w: 500, h: 13 }, { size: 8.4, color: C.navy, bold: true, align: "center", fill: C.paleAmber });

  addShape(slide, { x: 655, y: 430, w: 525, h: 172, fill: C.white, line: C.line });
  addText(slide, "Revised scenario equations", { x: 679, y: 448, w: 480, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  const scenarioRows = (adjusted.calculationDetails?.scenarioEquations || []).slice(0, 4).map((row) => [
    row.scenario,
    row.estimate,
  ]);
  addTable(slide, ["Scenario", "USD / month"], scenarioRows, { x: 680, y: 482, w: 470, h: 104 }, [1.7, 0.95], {
    rowH: 16,
    headerH: 20,
    fontSize: 6.6,
    cellStyle: (_cell, col, row) => col === 1 && row[0].includes("Budget") ? TOP_COST_CELL_STYLE : {},
  });
  addText(slide, "Basic Ingest assumes telemetry topics do not require app-side MQTT subscription.", { x: 682, y: 592, w: 465, h: 13 }, { size: 8.4, color: C.navy, bold: true, align: "center", fill: C.paleBlue });

  addShape(slide, { x: 70, y: 618, w: 495, h: 48, fill: C.white, line: C.line });
  addText(slide, "Top revised drivers: IoT Core, ECS/Fargate, ACM PCA, operational DB/S3 telemetry path, Support+.", { x: 92, y: 628, w: 450, h: 16 }, { size: 8.8, color: C.black, face: FONT_EN });
  addText(slide, "Sources: aws-pricing-sources.md + aws-review-adjustments.md + AWS review PDF", { x: 92, y: 646, w: 450, h: 14 }, { size: 8.2, color: C.muted, face: FONT_EN });

  addShape(slide, { x: 585, y: 618, w: 595, h: 48, fill: C.white, line: C.line });
  const caveats = [
    "Budget headline includes Business Support+; excludes tax, discounts, Savings Plans, Reserved Instances, Marketplace, and video/WebRTC/TURN.",
    "Actual AWS bill is not queried; revise again after AWS workload measurements and telemetry retention/query decisions.",
  ];
  caveats.forEach((c, i) => {
    addStatusDot(slide, "partial", 600, 628 + i * 18);
    addText(slide, c, { x: 620, y: 624 + i * 18, w: 540, h: 16 }, { size: 8.5, color: C.black, face: FONT_EN });
  });
  return slide;
}

async function slideAwsUserLoginFlow(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS User Login Data Flow", "MANAGED-SERVICE REQUEST PATH");
  addAwsFlowSlide(slide, {
    intro: "This view explains how a Portal/Admin/App user login would move through the AWS-native cost model. Cognito owns user authentication; application services still own tenant, role, audit, and product records.",
    steps: [
      ["User app / browser", "Portal, Admin, or mobile app starts sign-in and receives token/session state.", C.paleBlue],
      ["CloudFront / ALB", "TLS entry point, routing, WAF option, access logs, and static asset delivery.", C.paleTeal],
      ["Amazon Cognito", "Hosted login or SDK auth; validates password/social/OIDC flow and issues JWT.", C.paleAmber],
      ["Lambda / API service", "Validates token claims, maps user to tenant/RBAC, and runs app-specific login logic.", C.paleBlue],
      ["Amazon RDS", "Stores user profile, org membership, device ownership, audit, and application metadata.", C.paleTeal],
      ["Response", "JWT/session plus application profile returns to client; downstream APIs use the same auth context.", C.paleBlue],
    ].map(([title, body, fill]) => ({ title, body, fill })),
    sideTitle: "Side effects and cost-bearing services",
    sideItems: [
      ["CloudWatch Logs", "ALB/API/Lambda/application logs, login errors, audit traces, and security-event evidence."],
      ["Secrets / KMS", "App client secrets, DB credentials, token-signing material, and encrypted configuration."],
      ["Managed Prometheus", "Login/API latency, error rate, request volume, and service health metrics."],
      ["RDS backup", "User and tenant state remains recoverable through snapshots/backups and retention policy."],
    ],
    footer: "Cost implication: user login touches Cognito, API runtime, RDS, logging, metrics, and secrets; it is the main user-driven portion of the 5% user allocation.",
  });
  return slide;
}

async function slideAwsDeviceLoginFlow(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Device Login / Activation Flow", "DEVICE IDENTITY AND REGISTRY PATH");
  addAwsFlowSlide(slide, {
    intro: "Device login means activation, certificate/device identity validation, and binding the device to its tenant. CA signing is an issuance path; runtime validation is not a CloudHSM call.",
    steps: [
      ["Device SDK", "Factory credential, claim token, or mTLS certificate starts activation/login.", C.paleBlue],
      ["ALB / API edge", "TLS ingress for activation API, device API, and provisioning callback path.", C.paleTeal],
      ["Lambda / Video API", "Validates activation request, checks ownership intent, and creates runtime session facts.", C.paleBlue],
      ["IoT Core registry", "Registers or validates device identity, policy, certificate status, and MQTT permission model.", C.paleAmber],
      ["ACM PCA / KMS", "Issues device certificates; optional offline CloudHSM Root CA is only for CA ceremony.", C.paleTeal],
      ["RDS / Aurora", "Authoritative account/device registry, activation status, audit trail, and lifecycle records.", C.paleBlue],
    ].map(([title, body, fill]) => ({ title, body, fill })),
    sideTitle: "Operational records created during device login",
    sideItems: [
      ["CloudWatch Logs", "Activation attempts, certificate failures, policy rejects, and API errors."],
      ["IoT Device Mgmt", "Fleet indexing, search, jobs/commands, and managed device metadata if adopted."],
      ["S3 / backup", "Certificate artifacts, release manifests, firmware metadata, and selected offline exports."],
      ["Prometheus / alarms", "Activation success rate, reject taxonomy, latency, and retry pressure."],
    ],
    footer: "Cost implication: default CA signing uses ACM PCA / hybrid offline Root CA, not always-on CloudHSM runtime.",
  });
  return slide;
}

async function slideAwsMqttFlow(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS MQTT Runtime Data Flow", "DEVICE MESSAGING / SHADOW / OBSERVABILITY");
  addAwsFlowSlide(slide, {
    intro: "This page maps the 100K-device MQTT runtime to the AWS-review-adjusted model. High-volume telemetry is not modeled as per-message Lambda or primary RDS writes.",
    steps: [
      ["Device fleet", "100K devices maintain MQTT sessions and publish command/status/log/shadow messages.", C.paleBlue],
      ["IoT Core Basic Ingest", "Managed TLS identity and MQTT path; telemetry topics bypass broker fan-out where eligible.", C.paleAmber],
      ["Rules / Queue", "Routes selected topics to SQS/EventBridge or workers; shadow remains separate from raw telemetry.", C.paleTeal],
      ["Workers / APIs", "Handle control-plane and lower-frequency semantic events; not every telemetry message.", C.paleBlue],
      ["CloudWatch / S3", "Semantic logs, runtime logs, telemetry archives, and Athena-style offline analytics.", C.paleTeal],
      ["RDS / Aurora", "Operational metadata, registry, lifecycle, and selected offline sync only.", C.paleBlue],
    ].map(([title, body, fill]) => ({ title, body, fill })),
    sideTitle: "Runtime branches to size in the estimate",
    sideItems: [
      ["MQTT metering", "Connection minutes, Basic Ingest eligibility, payload size, shadow operations, and rules actions."],
      ["Device shadow", "Desired/reported state updates, conflict handling, cache/write-through behavior, and retention."],
      ["Logs / telemetry", "CloudWatch Logs or S3/Athena-style storage; K8S equivalent is Loki."],
      ["Back-pressure path", "SQS/EventBridge/NATS-equivalent decisions for retries, ordering, DLQ, and worker fan-out."],
    ],
    footer: "Cost implication: MQTT remains the largest device-driven path, but telemetry should not be costed as Lambda-per-message or RDS-per-message.",
  });
  return slide;
}

async function slideLinodeScaleEstimate(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS K8S Runtime Cost View", "SELF-OPERATED CLUSTER ESTIMATE");
  const estimate = payload.linodeScaleEstimate || {};
  const scenarios = estimate.scenarios || {};
  const perUnit = estimate.perUnit || {};
  const selfManagedPerDevice = deviceUnitCostOnly(perUnit.selfManagedPerDevice);
  const config = estimate.configuration || [];

  addText(slide, "AWS K8s billing is node-based in this EC2 worker profile: pods show workload placement, while cost comes from EKS control plane, EC2 worker nodes, EBS, LB, NAT/VPC, ECR, and S3.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 14.1, color: C.navy, bold: true, align: "center", fill: C.pale });

  const summary = [
    ["Sizing", "5,000 users / 100,000 devices"],
    ["Worker nodes", "14 EC2 nodes + EKS control plane"],
    ["Monthly infra estimate", scenarios.selfManaged || "2,733.00 USD"],
    ["Cost / device", selfManagedPerDevice],
  ];
  summary.forEach((item, i) => {
    const x = 70 + i * 292;
    addShape(slide, { x, y: 214, w: 250, h: 66, fill: i === 1 ? C.paleAmber : C.paleBlue, line: C.line });
    addText(slide, item[0], { x: x + 8, y: 224, w: 234, h: 14 }, { size: 8.5, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, item[1], { x: x + 8, y: 246, w: 234, h: 18 }, { size: 12, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });

  const configRows = config.map((row) => [
    row.role,
    row.count,
    row.plan.replace("G8 Dedicated ", "G8 "),
    row.monthlySubtotal,
    row.rationale,
  ]);
  addText(slide, "100K self-operated AWS K8s configuration", { x: 58, y: 310, w: 720, h: 22 }, { size: 15, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Role", "Count", "Plan", "USD / mo", "Why needed"], configRows, { x: 50, y: 342, w: 780, h: 260 }, [1.35, 0.55, 1.2, 0.55, 2.57], { rowH: 22, headerH: 24, fontSize: 6.35 });

  addShape(slide, { x: 860, y: 320, w: 360, h: 118, fill: "#FFF1F1", line: C.red });
  addText(slide, "K8S cost / device", { x: 884, y: 338, w: 312, h: 16 }, { size: 12.5, color: C.red, bold: true, align: "center", face: FONT_EN });
  addText(slide, selfManagedPerDevice, { x: 884, y: 362, w: 312, h: 34 }, { size: 24, color: "#B00020", bold: true, align: "center", face: FONT_EN });
  addText(slide, `${scenarios.selfManaged || "2,733.00 USD"} / 100,000 devices`, { x: 892, y: 404, w: 296, h: 12 }, { size: 8.4, color: C.red, bold: true, align: "center", face: FONT_EN });
  addText(slide, `${perUnit.selfManagedPerUser || "0.55 USD/user-month"}; ${perUnit.selfManagedUserWithTwentyDevices || "0.55 USD/month"} for 1 user + 20 devices`, { x: 892, y: 421, w: 296, h: 10 }, { size: 7.2, color: C.black, bold: true, align: "center", face: FONT_EN });

  addShape(slide, { x: 860, y: 466, w: 360, h: 118, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Interpretation", { x: 884, y: 486, w: 312, h: 20 }, { size: 16, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "EMQX, PostgreSQL, Loki, Grafana, Prometheus, Redis/Valkey, and NATS run as pods, but AWS charges this profile by the underlying nodes and infrastructure.", { x: 890, y: 515, w: 300, h: 48 }, { size: 9.7, color: C.black, align: "center" });

  addShape(slide, { x: 70, y: 620, w: 1130, h: 44, fill: C.white, line: C.line });
  addText(slide, "Caveats: AWS IoT Core, RDS, CloudWatch Logs, Managed Prometheus, Lambda primary runtime, ElastiCache, SQS, CloudHSM, external managed operations, and video/WebRTC/TURN are excluded.", { x: 92, y: 630, w: 1090, h: 16 }, { size: 8.5, color: C.black, bold: true, align: "center", face: FONT_EN });
  addText(slide, `Source: AWS self-operated K8s 100K reference estimate, collected ${estimate.collected || "n/a"}`, { x: 92, y: 650, w: 1090, h: 12 }, { size: 7.5, color: C.muted, align: "center", face: FONT_EN });
  return slide;
}

async function slideAwsUnitCost(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Unit Cost Per Month", "PER USER / PER DEVICE VIEW");
  const aws = payload.awsCostEstimate || {};
  const adjusted = aws.adjusted || {};
  const unitCosts = adjusted.unitCosts || aws.unitCosts || {};
  const basis = unitCosts.basis || {};

  addText(slide, "This page converts the AWS-review-adjusted monthly estimate into unit economics. The headline includes ACM PCA / hybrid CA and Business Support+; raw and weighted rows split the same monthly cost pool.", { x: 85, y: 154, w: 1110, h: 42 }, { size: 14.2, color: C.navy, bold: true, align: "center", fill: C.pale });

  const basisItems = [
    ["End users", basis.endUsers || "5,000"],
    ["Registered devices", basis.registeredDevices || "100,000"],
    ["Devices / user", basis.devicesPerUser || "20"],
    ["Allocation", `${basis.weightedUserPool || "5%"} user / ${basis.weightedDevicePool || "95%"} device`],
  ];
  basisItems.forEach((item, i) => {
    const x = 82 + i * 300;
    addShape(slide, { x, y: 218, w: 245, h: 62, fill: i === 3 ? C.paleAmber : C.paleBlue, line: C.line });
    addText(slide, item[0], { x: x + 8, y: 226, w: 229, h: 15 }, { size: 9, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, item[1], { x: x + 8, y: 248, w: 229, h: 22 }, { size: i === 3 ? 13 : 16, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });

  const defaultDeviceCost = adjusted.perUnit?.budgetPerDevice || "0.034 USD/device-month";
  const defaultDeviceCostNoSupport = adjusted.perUnit?.infraPerDevice || "0.027 USD/device-month";
  const budgetTotal = adjusted.scenarios?.budgetHeadline || "3,388.94 USD";
  addShape(slide, { x: 150, y: 294, w: 980, h: 62, fill: "#FFF1F1", line: C.red });
  addText(slide, "100K devices AWS review-adjusted cost allocation", { x: 170, y: 301, w: 940, h: 15 }, { size: 10.5, color: C.red, bold: true, align: "center", face: FONT_EN });
  addText(slide, `${defaultDeviceCost}  (${budgetTotal} / 100,000 devices)`, { x: 170, y: 319, w: 940, h: 24 }, { size: 20, color: "#B00020", bold: true, align: "center", face: FONT_EN });
  addText(slide, `Infra-only reference: ${defaultDeviceCostNoSupport}; headline includes ACM PCA / hybrid CA + Business Support+`, { x: 170, y: 342, w: 940, h: 10 }, { size: 7.8, color: C.red, bold: true, align: "center", face: FONT_EN });

  const rawRows = (unitCosts.rawDivision || []).map((row) => [
    row.scenario,
    row.monthlyTotal,
    row.perUserMonth,
    row.perDeviceMonth,
    row.notes,
  ]);
  addText(slide, "Raw unit cost: total monthly AWS estimate divided by fleet size", { x: 70, y: 372, w: 1120, h: 22 }, { size: 15, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Monthly total", "Per user / month", "Per device / month", "Notes"], rawRows, { x: 70, y: 402, w: 1140, h: 120 }, [1.45, 0.85, 0.95, 0.95, 1.45], { rowH: 21, headerH: 23, fontSize: 7.4 });

  const weightedRows = (unitCosts.weightedAllocation || []).map((row) => [
    row.scenario,
    row.perUserMonth,
    row.perDeviceMonth,
    row.effectiveUserWithTwentyDevices || row.effectiveUserWithTenDevices || row.effectiveUserWithFourDevices,
  ]);
  addText(slide, "Weighted unit cost: 5% user pool / 95% device pool", { x: 70, y: 542, w: 650, h: 22 }, { size: 15, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Per user / month", "Per device / month", "1 user + 20 devices"], weightedRows, { x: 70, y: 574, w: 700, h: 92 }, [1.7, 1.0, 1.0, 1.0], { rowH: 20, headerH: 22, fontSize: 7.3 });

  addShape(slide, { x: 810, y: 544, w: 380, h: 112, fill: C.paleTeal, line: C.line });
  addText(slide, "How to present it", { x: 832, y: 562, w: 336, h: 20 }, { size: 15, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "For budget approval, use the red raw division number. For business-model discussion, use the weighted device-heavy unit view. Do not add per-user and per-device raw rows together.", { x: 832, y: 591, w: 336, h: 44 }, { size: 9.6, color: C.black, align: "center" });
  return slide;
}

async function slideAwsCostCalculationBase(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Calculation Detail 1/3", "REVIEW-ADJUSTED SERVICE LINE ITEMS");
  const aws = payload.awsCostEstimate || {};
  const adjusted = aws.adjusted || {};
  const details = adjusted.calculationDetails || {};

  addText(slide, "This page shows the derived AWS review-adjusted estimate. Original pricing remains in aws-pricing-sources.md; this view applies architecture corrections from the AWS review and Realtek feedback.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 13.5, color: C.navy, bold: true, align: "center", fill: C.pale });

  const assumptions = [
    ["Region", aws.region || "ap-southeast-1"],
    ["Users / devices", "5,000 users / 100,000 devices"],
    ["Runtime month", "730 hours"],
    ["Currency", aws.currency || "USD"],
  ];
  assumptions.forEach((item, i) => {
    const x = 75 + i * 292;
    addShape(slide, { x, y: 210, w: 250, h: 52, fill: i === 0 ? C.paleTeal : C.paleBlue, line: C.line });
    addText(slide, item[0], { x: x + 8, y: 217, w: 234, h: 14 }, { size: 8.5, color: C.muted, bold: true, align: "center", face: FONT_EN });
    addText(slide, item[1], { x: x + 8, y: 238, w: 234, h: 16 }, { size: 12, color: C.navy, bold: true, align: "center", face: FONT_EN });
  });

  const baseRows = (details.baseLineItems || []).slice(0, 9).map((row) => [
    row.area,
    row.monthlyEstimate,
    row.notes,
  ]);
  addTable(slide, ["Revised service item", "USD / month", "Calculation / assumption"], baseRows, { x: 50, y: 285, w: 745, h: 310 }, [1.42, 0.7, 3.15], {
    rowH: 27,
    headerH: 23,
    fontSize: 6.25,
    cellStyle: (_cell, col, row) => col === 1 && AWS_TOP_COST_ITEMS.some((name) => row[0].includes(name)) ? TOP_COST_CELL_STYLE : {},
  });

  const adjustmentRows = (adjusted.adjustments || []).map((row) => [
    row.item.replace("AWS ", ""),
    row.original,
    row.revised,
    row.delta,
  ]);
  addText(slide, "Adjustment summary", { x: 810, y: 285, w: 360, h: 20 }, { size: 13, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Item", "Original", "Revised", "Delta"], adjustmentRows, { x: 810, y: 312, w: 390, h: 156 }, [1.45, 0.7, 0.7, 0.7], {
    rowH: 22,
    headerH: 22,
    fontSize: 6.2,
    cellStyle: (_cell, col) => col >= 2 ? TOP_COST_CELL_STYLE : {},
  });

  const driverRows = (adjusted.topDrivers || []).map((row) => [
    row.rank,
    row.item,
    row.monthlyEstimate,
  ]);
  addText(slide, "Top revised cost drivers", { x: 810, y: 490, w: 360, h: 20 }, { size: 13, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["#", "Item", "USD"], driverRows, { x: 810, y: 517, w: 390, h: 95 }, [0.25, 1.7, 0.65], {
    rowH: 15,
    headerH: 18,
    fontSize: 5.9,
    cellStyle: (_cell, col) => col === 2 ? TOP_COST_CELL_STYLE : {},
  });

  addShape(slide, { x: 70, y: 620, w: 1130, h: 44, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, details.cloudWatchFormula || "Telemetry is not modeled as primary RDS ingestion or per-message Lambda.", { x: 90, y: 631, w: 1090, h: 18 }, { size: 8.7, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, `Source: ${adjusted.source || "docs/cost/aws-review-adjustments.md"}; original pricing snapshot collected ${aws.collected || "n/a"}`, { x: 90, y: 650, w: 1090, h: 12 }, { size: 7.5, color: C.muted, align: "center", face: FONT_EN });
  return slide;
}

async function slideAwsEstimateReviewNotes(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Estimate Review Notes", "ENGINEERING ASSESSMENT");
  const aws = payload.awsCostEstimate || {};
  const adjusted = aws.adjusted || {};

  addText(slide, "Engineering view: the AWS proposal is reasonable as a review-adjusted planning model, but it is not yet a formal quote or production budget. The remaining risk is concentrated in workload-shape assumptions, not in arithmetic.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 13.8, color: C.navy, bold: true, align: "center", fill: C.pale });

  const acceptedRows = [
    ["CA / HSM", "Reasonable", "CloudHSM is not on the runtime validation path. ACM PCA / hybrid offline Root CA is a better default for device certificate issuance."],
    ["Telemetry path", "Reasonable", "Telemetry should not be modeled as primary RDS ingestion or per-message Lambda. Queue + CloudWatch/S3-style storage matches the revised architecture."],
    ["Device management", "Reasonable", "Removing Managed Integrations and keeping Fleet Indexing matches the expected device-management scope."],
    ["Lambda", "Reasonable", "30M API/control-plane invocations is acceptable as a planning line; it should not scale with every MQTT telemetry message."],
  ];
  addText(slide, "Accepted corrections", { x: 74, y: 224, w: 520, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Area", "Judgement", "Reason"], acceptedRows, { x: 64, y: 255, w: 555, h: 220 }, [0.9, 0.85, 2.7], {
    rowH: 43,
    headerH: 24,
    fontSize: 7.6,
    cellStyle: (_cell, col) => col === 1 ? { color: C.green, bold: true } : {},
  });

  const riskRows = [
    ["IoT Core Basic Ingest", "High", "924 USD depends on telemetry topics not requiring app-side MQTT subscription, retained messages, or broker fan-out."],
    ["Logs / telemetry retention", "Medium", "CloudWatch Logs at 48.18 USD may be low if semantic/runtime logs are retained longer, queried often, or used for telemetry-like volume."],
    ["Operational DB + S3 path", "Medium", "384 USD remains a placeholder until RDS/Aurora size, S3/Athena retention, and selected offline sync requirements are measured."],
    ["ECS Fargate sizing", "Medium", "539.79 USD is reasonable for planning, but replicas, HA, CPU/memory headroom, and always-on workers still need workload validation."],
  ];
  addText(slide, "Assumptions to verify before budget sign-off", { x: 680, y: 224, w: 500, h: 22 }, { size: 16, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Assumption", "Risk", "What to confirm"], riskRows, { x: 660, y: 255, w: 555, h: 220 }, [1.2, 0.55, 2.6], {
    rowH: 43,
    headerH: 24,
    fontSize: 7.35,
    cellStyle: (_cell, col, row) => col === 1 ? { color: row[1] === "High" ? C.red : C.amber, bold: true } : {},
  });

  addShape(slide, { x: 105, y: 520, w: 1070, h: 56, fill: "#FFF1F1", line: C.red });
  addText(slide, `Current planning headline remains ${adjusted.scenarios?.budgetHeadline || "3,388.94 USD"} / month, or ${adjusted.perUnit?.budgetPerDevice || "0.034 USD/device-month"} for 100,000 devices.`, { x: 130, y: 535, w: 1020, h: 20 }, { size: 15, color: "#B00020", bold: true, align: "center", face: FONT_EN });
  addText(slide, "Use this number for planning discussion only; confirm Basic Ingest eligibility, log retention/query behavior, DB/offline sync scope, and Fargate replica sizing before treating it as a budget baseline.", { x: 130, y: 557, w: 1020, h: 12 }, { size: 8.6, color: C.red, bold: true, align: "center", face: FONT_EN });

  addText(slide, "Source basis: docs/cost/aws-review-adjustments.md plus AWS review PDF; original public pricing snapshot remains docs/cost/aws-pricing-sources.md.", { x: 120, y: 642, w: 1040, h: 14 }, { size: 8.5, color: C.muted, align: "center", face: FONT_EN });
  return slide;
}

async function slideAwsCostFormulaBreakdown(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Calculation Detail 2/3", "UNIT PRICE FORMULA BREAKDOWN");
  const aws = payload.awsCostEstimate || {};
  const adjusted = aws.adjusted || {};
  const details = adjusted.calculationDetails || {};

  addText(slide, "This page expands the AWS-review-adjusted estimate. Source unit prices remain in the pricing snapshot; adjustments are applied in the derived review model.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 14.5, color: C.navy, bold: true, align: "center", fill: C.pale });

  const formulaRows = (details.formulaBreakdown || []).map((row) => [
    row.item,
    row.quantity,
    row.unitPrice,
    row.formula,
    row.estimate,
  ]);
  addTable(slide, ["Item", "Quantity", "Public unit price", "Formula", "USD / month"], formulaRows, { x: 42, y: 220, w: 1195, h: 365 }, [1.25, 1.45, 1.55, 2.15, 0.75], {
    rowH: 20,
    headerH: 24,
    fontSize: 5.8,
    cellStyle: (_cell, col, row) => col === 4 && AWS_TOP_COST_ITEMS.some((name) => row[0].includes(name)) ? TOP_COST_CELL_STYLE : {},
  });

  addShape(slide, { x: 66, y: 604, w: 540, h: 58, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Telemetry and RDS correction", { x: 86, y: 613, w: 230, h: 16 }, { size: 12, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "Telemetry is not modeled as all-in RDS writes. The revised model uses an operational DB plus CloudWatch/S3-style telemetry storage placeholder.", { x: 86, y: 636, w: 500, h: 18 }, { size: 8.6, color: C.black, face: FONT_EN });

  addShape(slide, { x: 650, y: 604, w: 560, h: 58, fill: C.paleBlue, line: C.line });
  addText(slide, "AI-assisted operations", { x: 670, y: 613, w: 240, h: 16 }, { size: 12, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "Lambda is still an API/control-plane planning line. It is not multiplied by every MQTT telemetry message.", { x: 670, y: 634, w: 520, h: 11 }, { size: 8.7, color: C.black, face: FONT_EN });
  addText(slide, "High-volume telemetry goes through Basic Ingest / Rules / queue / CloudWatch or S3-style storage.", { x: 670, y: 648, w: 520, h: 11 }, { size: 8.2, color: C.black, face: FONT_EN });
  return slide;
}

async function slideAwsCostCalculationScenarios(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Calculation Detail 3/3", "SCENARIOS / ROBUST / UNIT COST");
  const aws = payload.awsCostEstimate || {};
  const adjusted = aws.adjusted || {};
  const details = adjusted.calculationDetails || {};

  addText(slide, "This page explains how the revised scenario totals are derived: original source estimate is adjusted by AWS review findings, CA signing uses ACM PCA / hybrid CA, and the budget headline includes Business Support+.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 14.0, color: C.navy, bold: true, align: "center", fill: C.pale });

  const scenarioRows = (details.scenarioEquations || []).map((row) => [
    row.scenario,
    row.formula,
    row.estimate,
  ]);
  addText(slide, "Revised scenario equations", { x: 64, y: 218, w: 550, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Formula", "USD / month"], scenarioRows, { x: 58, y: 248, w: 670, h: 130 }, [1.4, 2.55, 0.85], { rowH: 22, headerH: 24, fontSize: 7.2 });

  const driverRows = (adjusted.topDrivers || []).map((row) => [
    row.rank,
    row.item,
    row.monthlyEstimate,
  ]);
  addText(slide, "Top revised drivers", { x: 760, y: 218, w: 440, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["#", "Item", "USD / month"], driverRows, { x: 760, y: 248, w: 450, h: 122 }, [0.25, 1.65, 0.75], {
    rowH: 18,
    headerH: 22,
    fontSize: 6.9,
    cellStyle: (_cell, col) => col === 2 ? TOP_COST_CELL_STYLE : {},
  });

  const supportRows = [
    ["Default + CA", adjusted.scenarios?.defaultWithCa || "3,109.12 USD", "9%", adjusted.scenarios?.businessSupportPlus || "279.82 USD"],
    ["Robust + CA", adjusted.scenarios?.robustWithCa || "3,917.17 USD", "9%", adjusted.scenarios?.robustBusinessSupportPlus || "352.55 USD"],
  ];
  addText(slide, "Business Support+ included in headline", { x: 58, y: 414, w: 620, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Gross charges", "Rate", "Support USD"], supportRows, { x: 58, y: 444, w: 670, h: 70 }, [1.25, 1.05, 0.45, 0.8], {
    rowH: 22,
    headerH: 22,
    fontSize: 7.0,
    cellStyle: (_cell, col) => col === 3 ? TOP_COST_CELL_STYLE : {},
  });
  addText(slide, "Budget headline includes Business Support+. It still excludes tax, discounts, Savings Plans, Reserved Instances, Marketplace, and camera/WebRTC/TURN.", { x: 82, y: 527, w: 620, h: 42 }, { size: 9.5, color: C.black, align: "center", fill: C.paleBlue });

  const unitRows = (adjusted.unitCosts?.rawDivision || []).map((row) => [
    row.scenario,
    `${row.monthlyTotal} / 5,000 = ${row.perUserMonth}`,
    `${row.monthlyTotal} / 100,000 = ${row.perDeviceMonth}`,
  ]);
  addText(slide, "Unit cost formulas", { x: 760, y: 454, w: 420, h: 20 }, { size: 14, color: C.navy, bold: true, face: FONT_EN });
  addTable(slide, ["Scenario", "Per user", "Per device"], unitRows, { x: 760, y: 484, w: 450, h: 112 }, [1.45, 1.25, 1.25], { rowH: 20, headerH: 22, fontSize: 6.7 });
  const defaultDeviceCost = adjusted.perUnit?.budgetPerDevice || "0.034 USD/device-month";
  addShape(slide, { x: 760, y: 606, w: 450, h: 28, fill: "#FFF1F1", line: C.red });
  addText(slide, `100K devices: ${defaultDeviceCost}`, { x: 780, y: 614, w: 410, h: 11 }, { size: 10.5, color: "#B00020", bold: true, align: "center", face: FONT_EN });
  addShape(slide, { x: 760, y: 641, w: 450, h: 30, fill: C.paleTeal, line: C.line });
  addText(slide, "Weighted allocation view: 5% user-driven account/app/admin costs; 95% device-driven MQTT, shadow, logs, firmware, certificate, storage, and device API workload.", { x: 780, y: 648, w: 410, h: 15 }, { size: 7.4, color: C.navy, bold: true, align: "center" });
  return slide;
}

async function slideAwsCostSourceUrls(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "AWS Cost Source URLs", "PRICING REFERENCES USED FOR COST ESTIMATE");

  addText(slide, "Official AWS pricing pages and Bulk Price List regional CSVs remain the source pricing snapshot. The adjusted estimate also uses the internal AWS review PDF as architecture feedback, not as a public price source.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 13.8, color: C.navy, bold: true, align: "center", fill: C.pale });

  const sourceRows = [
    ["AWS Bulk Price List API", "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/index.json", "Regional CSV source for EC2, RDS, S3, IoT, CloudWatch, ELB, VPC, ECR, Lambda, and other unit prices."],
    ["Amazon EC2", "https://aws.amazon.com/ec2/pricing/on-demand/", "Worker node and residual VM runtime reference."],
    ["Amazon EKS", "https://aws.amazon.com/eks/pricing/", "K8s control-plane and EKS runtime pricing reference."],
    ["Amazon EBS", "https://aws.amazon.com/ebs/pricing/", "Persistent volume / database PVC storage reference."],
    ["Elastic Load Balancing", "https://aws.amazon.com/elasticloadbalancing/pricing/", "ALB/NLB ingress and capacity-unit assumptions."],
    ["Amazon VPC", "https://aws.amazon.com/vpc/pricing/", "NAT Gateway, data processing, and VPC endpoint allowance."],
    ["Amazon ECR", "https://aws.amazon.com/ecr/pricing/", "Container image storage allowance."],
    ["Amazon S3", "https://aws.amazon.com/s3/pricing/", "Firmware, release artifact, backup, and non-camera object storage."],
    ["AWS Lambda", "https://aws.amazon.com/lambda/pricing/", "AWS-native API/worker request and GB-second sensitivity."],
    ["Amazon API Gateway", "https://aws.amazon.com/api-gateway/pricing/", "HTTP API front-door request pricing."],
    ["Amazon RDS", "https://aws.amazon.com/rds/postgresql/pricing/", "Managed PostgreSQL alternative and robust-profile comparison."],
    ["AWS IoT Core", "https://aws.amazon.com/iot-core/pricing/", "Managed MQTT, messaging, shadow, rules, and connection comparison."],
    ["AWS IoT Device Mgmt", "https://aws.amazon.com/iot-device-management/pricing/", "Fleet Indexing reference; Managed Integrations removed from revised model."],
    ["Amazon CloudWatch", "https://aws.amazon.com/cloudwatch/pricing/", "Logs, metrics, alarms, and managed logging comparison."],
    ["Amazon Managed Prometheus", "https://aws.amazon.com/prometheus/pricing/", "Managed metrics ingestion, collector, storage, and query pricing."],
    ["AWS Support", "https://aws.amazon.com/premiumsupport/pricing/", "Business Support+ and Enterprise Support adders."],
  ];

  addText(slide, "Source", { x: 62, y: 220, w: 210, h: 18 }, { size: 10, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "URL / estimate usage", { x: 284, y: 220, w: 330, h: 18 }, { size: 10, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "Source", { x: 666, y: 220, w: 210, h: 18 }, { size: 10, color: C.navy, bold: true, face: FONT_EN });
  addText(slide, "URL / estimate usage", { x: 888, y: 220, w: 330, h: 18 }, { size: 10, color: C.navy, bold: true, face: FONT_EN });

  sourceRows.forEach((row, i) => {
    const col = i < 8 ? 0 : 1;
    const localIndex = i % 8;
    const x = col === 0 ? 50 : 654;
    const y = 246 + localIndex * 47;
    addShape(slide, { x, y, w: 570, h: 40, fill: localIndex % 2 ? C.white : C.pale, line: C.line });
    addText(slide, row[0], { x: x + 10, y: y + 7, w: 198, h: 14 }, { size: 7.8, color: C.navy, bold: true, face: FONT_EN });
    addText(slide, row[1], { x: x + 220, y: y + 5, w: 332, h: 11 }, { size: 5.8, color: C.blue, face: FONT_EN });
    addText(slide, row[2], { x: x + 220, y: y + 19, w: 332, h: 14 }, { size: 6.3, color: C.black });
  });

  addShape(slide, { x: 70, y: 635, w: 1130, h: 30, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, "Tracked source files: docs/cost/aws-pricing-sources.md, docs/cost/aws-review-adjustments.md, and aws_report/Realtek_IoT_Cost_Review_Reply_v1.10 - 20260630.pdf.", { x: 92, y: 643, w: 1090, h: 12 }, { size: 8.4, color: C.navy, bold: true, align: "center", face: FONT_EN });
  return slide;
}

function addProviderCostMode(slide, mode, x, y, w, accentFill) {
  const rowH = mode.rows.length > 6 ? 19 : 22;
  addShape(slide, { x, y, w, h: 404, fill: C.white, line: C.line });
  addShape(slide, { x: x + 14, y: y + 14, w: w - 28, h: 72, fill: accentFill, line: C.line });
  addText(slide, mode.title, { x: x + 28, y: y + 22, w: w - 56, h: 18 }, { size: 14, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, mode.total, { x: x + 28, y: y + 47, w: w - 56, h: 18 }, { size: 15, color: mode.highlight ? "#B00020" : C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, mode.unit, { x: x + 28, y: y + 69, w: w - 56, h: 12 }, { size: 9.0, color: "#B00020", bold: true, align: "center", face: FONT_EN });

  addTable(slide, ["Cost item", "Monthly", "Basis"], mode.rows, { x: x + 14, y: y + 104, w: w - 28, h: 224 }, [1.25, 0.68, 2.35], {
    rowH,
    headerH: 22,
    fontSize: mode.rows.length > 6 ? 5.75 : 6.35,
    cellStyle: (_cell, col, row) => col === 1 && mode.topItems.some((name) => row[0].includes(name)) ? TOP_COST_CELL_STYLE : {},
  });

  addShape(slide, { x: x + 14, y: y + 344, w: w - 28, h: 44, fill: C.paleAmber, line: "#E3C25A" });
  addText(slide, mode.note, { x: x + 28, y: y + 354, w: w - 56, h: 22 }, { size: 7.7, color: C.black, bold: true, align: "center" });
}

async function slideGcpCostView(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "GCP 100K Device Cost View", "K8S RUNTIME / CLOUD SERVICES");

  addText(slide, "First-pass GCP planning view using the same 5,000-user / 100,000-device profile. GKE Standard is node-based; GCP has no active Cloud IoT Core equivalent, so MQTT remains self-hosted or requires a third-party quote.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 13.4, color: C.navy, bold: true, align: "center", fill: C.pale });

  addProviderCostMode(slide, {
    title: "Self-operated GKE Standard",
    total: "Approx. 2,550 USD/month",
    unit: "2,550 / 100,000 = 0.026 USD/device-month",
    highlight: false,
    topItems: ["Worker nodes", "PostgreSQL", "Network"],
    rows: [
      ["GKE cluster fee", "73", "0.10 USD/cluster-hour * 730 hours."],
      ["Worker nodes", "1,885", "System, API, EMQX, PostgreSQL, observability, Redis/NATS pools."],
      ["Persistent Disk", "150", "1,500 GB database + Loki/Prometheus/broker/cache PVC allowance."],
      ["Cloud Load Balancing", "90", "HTTPS ingress plus MQTT TCP/TLS ingress allowance."],
      ["Cloud NAT / network", "240", "Private subnet outbound, inter-service transfer, and edge allowance."],
      ["Artifact Registry / Cloud Storage", "110", "Container images, DB backup, Loki/archive, firmware, release artifacts."],
    ],
    note: "This is the closest match to the AWS K8S runtime slide: EMQX, PostgreSQL, Loki, Grafana, Prometheus, Redis, and NATS remain self-operated on GKE nodes.",
  }, 50, 218, 555, C.paleBlue);

  addProviderCostMode(slide, {
    title: "GCP services + self-hosted gaps",
    total: "Approx. 3,200-4,300 USD/month",
    unit: "3,200-4,300 / 100,000 = 0.032-0.043 USD/device-month",
    highlight: true,
    topItems: ["Cloud SQL", "EMQX", "Cloud Run"],
    rows: [
      ["Cloud Run / API Gateway", "300-450", "Account/admin APIs and light workers after handler refactor."],
      ["Cloud SQL PostgreSQL", "700-950", "Managed PostgreSQL, 1,000 GB storage, backup retention; HA not included."],
      ["EMQX on GKE/Compute", "750-1,000", "GCP IoT Core retired; MQTT broker remains self-hosted."],
      ["Device mgmt pods", "550-750", "Custom registry, jobs, shadow, command adapter, and certificate workflow."],
      ["Cloud Logging", "8-40", "66 GB/month log ingestion; first 50 GiB/project/month free in public pricing."],
      ["Managed Prometheus", "65-120", "432M samples/month plus query/storage sensitivity."],
      ["Memorystore / Pub/Sub / Storage", "300-520", "Redis-compatible cache, event bus, object backup, CDN/storage allowance."],
    ],
    note: "Fair comparison adds the services GCP does not replace natively: EMQX plus device registry/jobs/shadow/cert workflow remain self-hosted or quote-based.",
  }, 675, 218, 555, C.paleTeal);

  addText(slide, "Sources: https://cloud.google.com/kubernetes-engine/pricing | https://docs.cloud.google.com/billing/docs/reference/rest/v1/services.skus/list | https://cloud.google.com/sql/pricing | https://cloud.google.com/run/pricing | https://cloud.google.com/products/observability/pricing", { x: 80, y: 632, w: 1120, h: 10 }, { size: 5.4, color: C.muted, align: "center", face: FONT_EN });
  addText(slide, "Also used: https://cloud.google.com/pubsub/pricing | https://cloud.google.com/memorystore/pricing | https://cloud.google.com/storage/pricing. Excludes tax, committed-use discounts, support, marketplace/private offers, and video/WebRTC/TURN.", { x: 80, y: 646, w: 1120, h: 10 }, { size: 5.4, color: C.muted, align: "center", face: FONT_EN });
  return slide;
}

async function slideAzureCostView(p, payload) {
  const slide = p.slides.add();
  await addBackground(slide, payload);
  await addHeader(slide, payload, "Azure 100K Device Cost View", "K8S RUNTIME / CLOUD SERVICES");

  addText(slide, "First-pass Azure planning view using the same 5,000-user / 100,000-device profile. AKS runtime cost is driven by VM node pools and supporting resources; Azure IoT Hub is the managed MQTT/device-ingestion comparison path.", { x: 82, y: 152, w: 1120, h: 42 }, { size: 13.4, color: C.navy, bold: true, align: "center", fill: C.pale });

  addProviderCostMode(slide, {
    title: "Self-operated AKS",
    total: "Approx. 3,100 USD/month",
    unit: "3,100 / 100,000 = 0.031 USD/device-month",
    highlight: false,
    topItems: ["Worker nodes", "PostgreSQL", "Network"],
    rows: [
      ["AKS Standard tier", "73", "Planning allowance for SLA-backed cluster management."],
      ["Worker nodes", "2,397", "System, API, EMQX, PostgreSQL, observability, Redis/NATS node pools."],
      ["Managed Disks", "150", "1,500 GB PostgreSQL + Loki/Prometheus/broker/cache PVC allowance."],
      ["Load Balancer / App Gateway", "120", "HTTPS ingress plus MQTT TCP/TLS ingress allowance."],
      ["NAT / bandwidth / network", "260", "Private outbound, inter-service transfer, and edge allowance."],
      ["ACR / Blob backup", "110", "Container registry, DB backup, Loki/archive, firmware, release artifacts."],
    ],
    note: "This is the Azure equivalent of the AWS K8S runtime slide: EMQX, PostgreSQL, Loki, Grafana, Prometheus, Redis, and NATS remain self-operated on AKS nodes.",
  }, 50, 218, 555, C.paleBlue);

  addProviderCostMode(slide, {
    title: "Azure cloud services alternative",
    total: "Approx. 3,800-5,100 USD/month",
    unit: "3,800-5,100 / 100,000 = 0.038-0.051 USD/device-month",
    highlight: true,
    topItems: ["IoT Hub", "PostgreSQL", "Monitor"],
    rows: [
      ["Azure IoT Hub", "1,500-2,500", "100K usually-online devices; 4 KB message chunks; S2/S3 units to size."],
      ["Container Apps / Functions", "300-500", "Account/device/admin APIs and light workers after handler refactor."],
      ["Azure PostgreSQL Flexible", "700-1,000", "Managed PostgreSQL, 1,000 GB storage, backup retention; HA not included."],
      ["Azure Monitor / Prometheus", "200-380", "Logs ingestion plus managed Prometheus sample/query sensitivity."],
      ["Device mgmt adapters", "350-500", "DPS, jobs/commands adapter, cert workflow, custom shadow semantics not fully native."],
      ["Azure Cache / Service Bus", "120-260", "Redis-compatible cache plus event bus/DLQ comparison."],
      ["Storage / ACR / networking", "300-450", "Blob backup, registry, CDN/storage, load balancer, NAT/bandwidth allowance."],
    ],
    note: "Fair comparison adds adapters around IoT Hub for provisioning, command/jobs flow, cert lifecycle, and any custom shadow semantics not covered natively.",
  }, 675, 218, 555, C.paleTeal);

  addText(slide, "Sources: https://azure.microsoft.com/pricing/details/kubernetes-service/ | https://learn.microsoft.com/rest/api/cost-management/retail-prices/azure-retail-prices | https://learn.microsoft.com/azure/iot-hub/iot-hub-devguide-pricing", { x: 80, y: 632, w: 1120, h: 10 }, { size: 5.4, color: C.muted, align: "center", face: FONT_EN });
  addText(slide, "Also used: https://azure.microsoft.com/pricing/details/postgresql/flexible-server/ | https://azure.microsoft.com/pricing/details/monitor/ | https://azure.microsoft.com/pricing/details/cache/ | https://azure.microsoft.com/pricing/details/container-apps/ | https://azure.microsoft.com/pricing/details/storage/blobs/", { x: 80, y: 646, w: 1120, h: 10 }, { size: 5.4, color: C.muted, align: "center", face: FONT_EN });
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
  addText(slide, "THANK YOU", { x: 260, y: 295, w: 760, h: 64 }, { size: 52, color: C.navy, bold: true, align: "center", face: FONT_EN });
  addText(slide, "www.realtek.com", { x: 500, y: 610, w: 280, h: 26 }, { size: 16, color: C.blue, bold: true, align: "center", face: FONT_EN });
  return slide;
}

const SLIDES = [
  slide01, slideMajorTopics, slide07, slideWhyCloud, slideCustomerUseCaseFit, slide03, slideCloudTypes, slideOperationalTransition, slide02, slide04, slideReleaseGateDefinition, slide05, slideLoadTestCapacityResult, slideLoadTestResourceCharts, slideLoadTestDecisionBasis, slide06, slide08,
  slidePortalTransition, slidePortalIntro, slide09, slideTechnicalTransition, slide10, slide11, slideStrideOverview, slide12, slideHsmSignerDesign, slide13,
  slideEvidenceTransition, slide14, slideCostView, slideAwsUserLoginFlow, slideAwsDeviceLoginFlow, slideAwsMqttFlow, slideLinodeScaleEstimate, slideAwsUnitCost, slideAwsCostCalculationBase, slideAwsEstimateReviewNotes, slideAwsCostFormulaBreakdown, slideAwsCostCalculationScenarios, slideAwsCostSourceUrls, slideGcpCostView, slideAzureCostView, slide16, slide17, slide18, slide19, slidePostAlphaCoverage, slide20, slide21,
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
