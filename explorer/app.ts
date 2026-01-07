// Source for explorer/app.js. Run "npm run build" in explorer/ to regenerate.
type NodeSummary = {
  address: string;
  status?: string;
  info?: string;
  current_height?: number;
  last_accepted_height?: number;
  latency_ms?: number;
  error?: string;
  block?: BlockSummary;
  frost_metrics?: FrostMetrics;
};

type BlockSummary = {
  height: number;
  block_hash?: string;
  prev_block_hash?: string;
  txs_hash?: string;
  miner?: string;
  tx_count: number;
  tx_type_counts?: Record<string, number>;
  accumulated_reward?: string;
  window?: number;
};

type FrostMetrics = {
  heap_alloc: number;
  heap_sys: number;
  num_goroutine: number;
  frost_jobs: number;
  frost_withdraws: number;
};

type NodesResponse = {
  base_port: number;
  count: number;
  nodes: string[];
};

type SummaryRequest = {
  nodes: string[];
  include_block: boolean;
  include_frost: boolean;
};

type SummaryResponse = {
  generated_at?: string;
  nodes: NodeSummary[];
  errors?: string[];
  selected?: string[];
  elapsed_ms?: number;
};

type ExplorerState = {
  nodes: string[];
  customNodes: string[];
  selected: Set<string>;
  auto: boolean;
  includeBlock: boolean;
  includeFrost: boolean;
  intervalMs: number;
  timer: number | null;
};

const state: ExplorerState = {
  nodes: [],
  customNodes: [],
  selected: new Set(),
  auto: false,
  includeBlock: false,
  includeFrost: false,
  intervalMs: 5000,
  timer: null,
};

const els = {
  refreshBtn: document.getElementById("refreshBtn") as HTMLButtonElement,
  autoToggle: document.getElementById("autoToggle") as HTMLInputElement,
  intervalSelect: document.getElementById("intervalSelect") as HTMLSelectElement,
  includeBlock: document.getElementById("includeBlock") as HTMLInputElement,
  includeFrost: document.getElementById("includeFrost") as HTMLInputElement,
  nodeInput: document.getElementById("nodeInput") as HTMLInputElement,
  addNodeBtn: document.getElementById("addNodeBtn") as HTMLButtonElement,
  selectAllBtn: document.getElementById("selectAllBtn") as HTMLButtonElement,
  clearBtn: document.getElementById("clearBtn") as HTMLButtonElement,
  nodeList: document.getElementById("nodeList") as HTMLDivElement,
  nodeCount: document.getElementById("nodeCount") as HTMLSpanElement,
  cards: document.getElementById("cards") as HTMLDivElement,
  lastUpdate: document.getElementById("lastUpdate") as HTMLSpanElement,
  statusLine: document.getElementById("statusLine") as HTMLSpanElement,
  elapsedLine: document.getElementById("elapsedLine") as HTMLSpanElement,
  defaultInfo: document.getElementById("defaultInfo") as HTMLSpanElement,
};

const storageKeys = {
  custom: "dex_explorer_custom_nodes",
  selected: "dex_explorer_selected_nodes",
};

const numberFormat = new Intl.NumberFormat("en-US");

init();

function init(): void {
  loadStoredState();
  bindEvents();
  loadDefaults();
}

function bindEvents(): void {
  els.refreshBtn.addEventListener("click", refreshSummary);
  els.autoToggle.addEventListener("change", (event) => {
    state.auto = (event.target as HTMLInputElement).checked;
    scheduleAuto();
  });
  els.intervalSelect.addEventListener("change", (event) => {
    state.intervalMs = Number((event.target as HTMLSelectElement).value);
    scheduleAuto();
  });
  els.includeBlock.addEventListener("change", (event) => {
    state.includeBlock = (event.target as HTMLInputElement).checked;
  });
  els.includeFrost.addEventListener("change", (event) => {
    state.includeFrost = (event.target as HTMLInputElement).checked;
  });
  els.addNodeBtn.addEventListener("click", addNode);
  els.selectAllBtn.addEventListener("click", selectAll);
  els.clearBtn.addEventListener("click", clearSelection);
}

function loadStoredState(): void {
  state.customNodes = safeParse(storageKeys.custom, []);
  const storedSelected = safeParse(storageKeys.selected, []);
  state.selected = new Set(storedSelected);
}

function loadDefaults(): void {
  setStatus("Loading defaults...");
  fetch("/api/nodes")
    .then((resp) => resp.json())
    .then((data: NodesResponse) => {
      const defaults = Array.isArray(data.nodes) ? data.nodes : [];
      state.nodes = mergeNodes(defaults, state.customNodes);
      if (state.selected.size === 0 && state.nodes.length > 0) {
        state.nodes.slice(0, 3).forEach((node) => state.selected.add(node));
      }
      renderNodes();
      els.defaultInfo.textContent = `Default ${data.base_port} + ${data.count}`;
      setStatus("Defaults ready");
    })
    .catch((err: Error) => {
      setStatus(`Failed to load defaults: ${err.message}`);
      renderNodes();
    });
}

function mergeNodes(defaults: string[], custom: string[]): string[] {
  const set = new Set<string>();
  defaults.forEach((node) => set.add(normalizeNode(node)));
  custom.forEach((node) => set.add(normalizeNode(node)));
  return Array.from(set).filter(Boolean);
}

function normalizeNode(value: string): string {
  return String(value || "")
    .trim()
    .replace(/^https?:\/\//, "")
    .replace(/\/$/, "");
}

function renderNodes(): void {
  els.nodeList.innerHTML = "";
  state.nodes.forEach((node) => {
    const label = document.createElement("label");
    label.className = "node-item";

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.checked = state.selected.has(node);
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) {
        state.selected.add(node);
      } else {
        state.selected.delete(node);
      }
      persistSelection();
      updateSelectionCount();
    });

    const text = document.createElement("span");
    text.textContent = node;

    label.appendChild(checkbox);
    label.appendChild(text);
    els.nodeList.appendChild(label);
  });
  updateSelectionCount();
}

function updateSelectionCount(): void {
  els.nodeCount.textContent = `${state.selected.size} selected`;
}

function addNode(): void {
  const value = normalizeNode(els.nodeInput.value);
  if (!value) {
    return;
  }
  if (!state.nodes.includes(value)) {
    state.nodes.push(value);
    state.customNodes.push(value);
    persistCustomNodes();
  }
  state.selected.add(value);
  persistSelection();
  els.nodeInput.value = "";
  renderNodes();
}

function selectAll(): void {
  state.nodes.forEach((node) => state.selected.add(node));
  persistSelection();
  renderNodes();
}

function clearSelection(): void {
  state.selected.clear();
  persistSelection();
  renderNodes();
}

function persistCustomNodes(): void {
  localStorage.setItem(storageKeys.custom, JSON.stringify(state.customNodes));
}

function persistSelection(): void {
  localStorage.setItem(
    storageKeys.selected,
    JSON.stringify(Array.from(state.selected))
  );
  updateSelectionCount();
}

function refreshSummary(): void {
  if (state.selected.size === 0) {
    renderEmptyState("Select at least one node to refresh.");
    return;
  }

  setStatus("Refreshing...");
  const payload: SummaryRequest = {
    nodes: Array.from(state.selected),
    include_block: state.includeBlock,
    include_frost: state.includeFrost,
  };

  fetch("/api/summary", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
    .then((resp) => resp.json())
    .then((data: SummaryResponse) => {
      renderSummary(data);
      setStatus("Snapshot updated");
    })
    .catch((err: Error) => {
      renderEmptyState(`Failed to load summary: ${err.message}`);
      setStatus("Snapshot failed");
    });
}

function renderSummary(data: SummaryResponse): void {
  els.cards.innerHTML = "";
  const nodes = Array.isArray(data.nodes) ? data.nodes : [];
  if (nodes.length === 0) {
    renderEmptyState("No data returned.");
    return;
  }

  nodes.forEach((node, index) => {
    const card = document.createElement("div");
    card.className = "card";
    card.style.animationDelay = `${index * 30}ms`;

    const header = document.createElement("h3");
    header.textContent = node.address || "unknown";

    const statusLine = document.createElement("div");
    statusLine.className = "status-line";
    const dot = document.createElement("span");
    dot.className = `status-dot ${statusClass(node)}`;
    const statusText = document.createElement("span");
    statusText.textContent = node.error
      ? "error"
      : node.status || "unknown";
    statusLine.appendChild(dot);
    statusLine.appendChild(statusText);

    const meta = document.createElement("div");
    meta.className = "meta";
    meta.appendChild(kv("Current height", formatNumber(node.current_height)));
    meta.appendChild(
      kv("Last accepted", formatNumber(node.last_accepted_height))
    );
    meta.appendChild(
      kv(
        "Height delta",
        formatNumber(
          Math.max(
            0,
            (node.current_height ?? 0) - (node.last_accepted_height ?? 0)
          )
        )
      )
    );
    meta.appendChild(kv("Latency", `${node.latency_ms || 0} ms`));

    if (node.info) {
      meta.appendChild(kv("Info", node.info));
    }
    if (node.error) {
      meta.appendChild(kv("Error", node.error));
    }

    if (node.block) {
      const block = node.block;
      meta.appendChild(kv("Block hash", truncate(block.block_hash)));
      meta.appendChild(kv("Miner", block.miner || "-"));
      meta.appendChild(kv("Tx count", formatNumber(block.tx_count)));
      if (block.accumulated_reward) {
        meta.appendChild(kv("Reward", block.accumulated_reward));
      }
    }

    if (node.frost_metrics) {
      const frost = node.frost_metrics;
      meta.appendChild(
        kv(
          "Frost jobs",
          `${formatNumber(frost.frost_jobs)} | ${formatNumber(
            frost.frost_withdraws
          )}`
        )
      );
      meta.appendChild(kv("Goroutines", formatNumber(frost.num_goroutine)));
    }

    card.appendChild(header);
    card.appendChild(statusLine);
    card.appendChild(meta);
    els.cards.appendChild(card);
  });

  els.lastUpdate.textContent = data.generated_at
    ? `Updated ${data.generated_at}`
    : "Updated";
  els.elapsedLine.textContent = data.elapsed_ms
    ? `Elapsed ${data.elapsed_ms} ms`
    : "";
}

function renderEmptyState(message: string): void {
  els.cards.innerHTML = `<div class="empty-state">${message}</div>`;
}

function statusClass(node: NodeSummary): string {
  if (node.error) return "status-bad";
  if (node.status && node.status.toLowerCase() === "ok") return "status-good";
  return "status-warn";
}

function kv(label: string, value: string): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "kv";
  const left = document.createElement("span");
  left.textContent = label;
  const right = document.createElement("span");
  right.textContent = value || "-";
  row.appendChild(left);
  row.appendChild(right);
  return row;
}

function truncate(value?: string, max = 16): string {
  if (!value) return "-";
  if (value.length <= max) return value;
  return `${value.slice(0, max)}...`;
}

function formatNumber(value?: number | string | null): string {
  if (value === undefined || value === null) return "-";
  if (typeof value === "string") return value;
  return numberFormat.format(value);
}

function scheduleAuto(): void {
  if (state.timer) {
    clearInterval(state.timer);
    state.timer = null;
  }
  if (state.auto) {
    state.timer = setInterval(refreshSummary, state.intervalMs);
  }
}

function setStatus(text: string): void {
  els.statusLine.textContent = text;
}

function safeParse<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as T;
    return parsed ?? fallback;
  } catch {
    return fallback;
  }
}
