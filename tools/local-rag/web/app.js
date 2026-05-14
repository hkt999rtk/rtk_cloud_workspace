const state = {
  status: null,
};

const $ = (id) => document.getElementById(id);

function shortSha(sha) {
  return sha && sha !== "unknown" ? sha.slice(0, 12) : "unknown";
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || response.statusText);
  return data;
}

async function refreshStatus() {
  state.status = await api("/api/status");
  $("docCount").textContent = state.status.active_documents;
  $("chunkCount").textContent = state.status.active_chunks;
  $("dirtyCount").textContent = state.status.dirty_repositories.length;
  renderRepos(state.status.repositories);
  populateRepoFilter(state.status.repositories);
}

function renderRepos(repos) {
  $("repoList").innerHTML = repos.map((repo) => `
    <div class="repo-item ${repo.dirty ? "dirty" : ""}">
      <strong>${repo.name}</strong>
      <div>${repo.path}</div>
      <code>${shortSha(repo.commit_sha)} · ${repo.branch_or_detached_state}</code>
    </div>
  `).join("");
}

function populateRepoFilter(repos) {
  const select = $("repoFilter");
  const current = select.value;
  const options = ['<option value="">All repositories</option>']
    .concat(repos.map((repo) => `<option value="${repo.name}">${repo.name}</option>`));
  select.innerHTML = options.join("");
  select.value = current;
}

function filters() {
  return {
    repo: $("repoFilter").value,
    source_layer: $("layerFilter").value,
    classification: $("classFilter").value,
  };
}

async function runQuery(event) {
  event?.preventDefault();
  const query = $("queryInput").value.trim();
  if (!query) return;
  $("queryState").textContent = "Searching";
  $("answerText").innerHTML = "";
  try {
    const result = await api("/api/query", {
      method: "POST",
      body: JSON.stringify({ query, filters: filters() }),
    });
    $("answerText").innerHTML = renderMarkdown(result.answer);
    $("queryState").textContent = "Done";
    $("confidenceNotes").textContent = result.confidence_notes.join(" · ");
    renderCitations(result.citations);
    renderMatches(result.matched_chunks);
  } catch (error) {
    $("queryState").textContent = "Error";
    $("answerText").innerHTML = `<p>${escapeHtml(error.message)}</p>`;
  }
}

function renderCitations(citations) {
  $("citationCount").textContent = citations.length;
  $("citations").innerHTML = citations.map((item, index) => `
    <div class="citation">
      <div class="path">[${index + 1}] ${item.path}</div>
      <div class="meta">
        lines ${item.lines[0]}-${item.lines[1]} · ${item.repo} · ${shortSha(item.commit)}
      </div>
      <div class="meta">
        <span class="badge ${item.source_layer}">${item.source_layer}</span>
        <span class="badge">${item.classification}</span>
      </div>
    </div>
  `).join("");
}

function renderMatches(chunks) {
  $("matches").innerHTML = chunks.map((item) => `
    <div class="match">
      <div class="path">${item.file_path}:${item.line_start}-${item.line_end}</div>
      <div class="meta">${item.heading} · score ${Number(item.score).toFixed(3)}</div>
      <div class="markdown-body compact">${renderMarkdown(trimMarkdown(item.content, 900))}</div>
    </div>
  `).join("");
}

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[char]));
}

function trimMarkdown(value, maxLength) {
  if (value.length <= maxLength) return value;
  return `${value.slice(0, maxLength).replace(/\s+\S*$/, "")}\n\n...`;
}

function renderMarkdown(markdown) {
  const lines = String(markdown || "").replace(/\r\n/g, "\n").split("\n");
  const html = [];
  let paragraph = [];
  let listItems = [];
  let code = [];
  let inCode = false;

  const flushParagraph = () => {
    if (!paragraph.length) return;
    html.push(`<p>${renderInline(paragraph.join(" "))}</p>`);
    paragraph = [];
  };
  const flushList = () => {
    if (!listItems.length) return;
    html.push(`<ul>${listItems.map((item) => `<li>${renderInline(item)}</li>`).join("")}</ul>`);
    listItems = [];
  };
  const flushCode = () => {
    html.push(`<pre><code>${escapeHtml(code.join("\n"))}</code></pre>`);
    code = [];
  };

  for (const rawLine of lines) {
    const line = rawLine.replace(/\s+$/, "");
    if (/^```/.test(line.trim())) {
      if (inCode) {
        flushCode();
        inCode = false;
      } else {
        flushParagraph();
        flushList();
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      code.push(rawLine);
      continue;
    }
    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }
    const heading = /^(#{1,4})\s+(.+)$/.exec(line);
    if (heading) {
      flushParagraph();
      flushList();
      const level = Math.min(heading[1].length + 2, 5);
      html.push(`<h${level}>${renderInline(heading[2])}</h${level}>`);
      continue;
    }
    const bullet = /^\s*[-*]\s+(.+)$/.exec(line);
    if (bullet) {
      flushParagraph();
      listItems.push(bullet[1]);
      continue;
    }
    const numbered = /^\s*\d+\.\s+(.+)$/.exec(line);
    if (numbered) {
      flushParagraph();
      listItems.push(numbered[1]);
      continue;
    }
    paragraph.push(line.trim());
  }
  if (inCode) flushCode();
  flushParagraph();
  flushList();
  return html.join("");
}

function renderInline(value) {
  let text = escapeHtml(value);
  text = text.replace(/`([^`]+)`/g, "<code>$1</code>");
  text = text.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  text = text.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, label, href) => {
    const safeHref = String(href).startsWith("#") ? href : "#";
    return `<a href="${safeHref}">${label}</a>`;
  });
  return text;
}

async function reindex(path, button) {
  button.disabled = true;
  button.textContent = "Indexing...";
  try {
    await api(path, { method: "POST", body: "{}" });
    await refreshStatus();
  } finally {
    button.disabled = false;
    button.textContent = path.includes("full") ? "Full reindex" : "Index changed";
  }
}

$("queryForm").addEventListener("submit", runQuery);
$("changedIndexButton").addEventListener("click", () => reindex("/api/index/changed", $("changedIndexButton")));
$("fullIndexButton").addEventListener("click", () => reindex("/api/index/full", $("fullIndexButton")));

refreshStatus().catch((error) => {
  $("queryState").textContent = "Status error";
  $("answerText").innerHTML = `<p>${escapeHtml(error.message)}</p>`;
});
