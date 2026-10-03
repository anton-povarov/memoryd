(() => {
  "use strict";

  const apiBase = "/api/v0";
  const maxBlobBytes = 100 * 1024 * 1024;
  const maxAutomaticPreviewBytes = 25 * 1024 * 1024;

  let markdown;
  try {
    markdown = new window.markdownit({
      html: false,
      linkify: false,
      typographer: false,
    });
    markdown.renderer.rules.image = (tokens, index, options, env, renderer) =>
      markdown.utils.escapeHtml(
        renderer.renderInlineAsText(tokens[index].children, options, env),
      );
  } catch {
    // Markdown rendering falls back to exact source if the dependency is unavailable.
  }

  function renderMarkdown(content) {
    const rendered = document.createElement("div");
    rendered.className = "artifact-markdown";
    try {
      if (!markdown) throw new Error("Markdown unavailable");
      const frontmatter = content.match(
        /^(\uFEFF?---[ \t]*\r?\n(?:[^\n]*\n)*?(?:---|\.\.\.)[ \t]*)(?:\r?\n|$)/,
      );
      rendered.innerHTML = markdown.render(
        frontmatter ? content.slice(frontmatter[0].length) : content,
      );
      if (frontmatter) {
        const source = document.createElement("pre");
        source.textContent = frontmatter[1];
        rendered.prepend(source);
      }
    } catch {
      const message = document.createElement("p");
      message.textContent = "Could not render Markdown.";
      const source = document.createElement("pre");
      source.textContent = content;
      rendered.replaceChildren(message, source);
    }
    return rendered;
  }

  const state = {
    memories: [],
    nextCursor: null,
    selectedID: null,
    previewURL: null,
    selectedFiles: [],
    importing: false,
    importComplete: false,
    nextQueueID: 1,
    loadingMore: false,
    understanding: null,
    displayedRunID: null,
    understandingSection: null,
    detailGeneration: 0,
    refreshTimer: null,
    refreshing: false,
    understandingReload: false,
    searchResponse: null,
    searchGeneration: 0,
    searchLoading: false,
    searchLoadingMore: false,
    searchVisible: false,
    searchScrollTop: 0,
    searchPhrase: "",
    detailFromSearch: false,
  };

  const elements = {
    vaultStatus: document.querySelector("#vault-status"),
    vaultStatusLabel: document.querySelector("#vault-status-label"),
    memoryCount: document.querySelector("#memory-count"),
    searchForm: document.querySelector("#search-form"),
    searchQuery: document.querySelector("#search-query"),
    memoryList: document.querySelector("#memory-list"),
    loadMore: document.querySelector("#load-more"),
    searchView: document.querySelector("#search-view"),
    searchPlan: document.querySelector("#search-plan"),
    searchStatus: document.querySelector("#search-status"),
    searchError: document.querySelector("#search-error"),
    searchResults: document.querySelector("#search-results"),
    searchLoadMore: document.querySelector("#search-load-more"),
    welcomeState: document.querySelector("#welcome-state"),
    welcomeEyebrow: document.querySelector("#welcome-eyebrow"),
    welcomeTitle: document.querySelector("#welcome-title"),
    welcomeCopy: document.querySelector("#welcome-copy"),
    welcomeImport: document.querySelector("#welcome-import"),
    welcomeNote: document.querySelector("#welcome-note"),
    memoryDetail: document.querySelector("#memory-detail"),
    detailLoading: document.querySelector("#detail-loading"),
    detailTitle: document.querySelector("#detail-title"),
    detailPills: document.querySelector("#detail-pills"),
    downloadMemory: document.querySelector("#download-memory"),
    originalTab: document.querySelector("#original-tab"),
    understandingTab: document.querySelector("#understanding-tab"),
    originalPanel: document.querySelector("#original-panel"),
    understandingPanel: document.querySelector("#understanding-panel"),
    understandingStatus: document.querySelector("#understanding-status"),
    refreshUnderstanding: document.querySelector("#refresh-understanding"),
    rebuildNoteToggle: document.querySelector("#rebuild-note-toggle"),
    rebuildNotePopover: document.querySelector("#rebuild-note-popover"),
    rebuildNoteForm: document.querySelector("#rebuild-note-form"),
    rebuildUserNote: document.querySelector("#rebuild-user-note"),
    rebuildNoteSubmit: document.querySelector("#rebuild-note-submit"),
    understandingUserNote: document.querySelector("#understanding-user-note"),
    understandingRefreshError: document.querySelector(
      "#understanding-refresh-error",
    ),
    understandingAttempt: document.querySelector("#understanding-attempt"),
    understandingRunSummary: document.querySelector(
      "#understanding-run-summary",
    ),
    understandingCompletedAt: document.querySelector(
      "#understanding-completed-at",
    ),
    understandingRunDetails: document.querySelector(
      "#understanding-run-details",
    ),
    understandingArtifacts: document.querySelector("#understanding-artifacts"),
    understandingSections: document.querySelector("#understanding-sections"),
    understandingArtifactHeading: document.querySelector(
      "#understanding-artefacts-heading",
    ),
    understandingArtifactSections: document.querySelector(
      "#understanding-artifact-sections",
    ),
    understandingDiagnostics: document.querySelector(
      "#understanding-diagnostics",
    ),
    previewStage: document.querySelector("#preview-stage"),
    metadataList: document.querySelector("#metadata-list"),
    detailMemoryId: document.querySelector("#detail-memory-id"),
    copyMemoryId: document.querySelector("#copy-memory-id"),
    detailHash: document.querySelector("#detail-hash"),
    copyHash: document.querySelector("#copy-hash"),
    backToList: document.querySelector("#back-to-list"),
    backToSearch: document.querySelector("#back-to-search"),
    importDialog: document.querySelector("#import-dialog"),
    importForm: document.querySelector("#import-form"),
    closeImport: document.querySelector("#close-import"),
    dropZone: document.querySelector("#drop-zone"),
    fileInput: document.querySelector("#file-input"),
    fileQueue: document.querySelector("#file-queue"),
    fileQueueTitle: document.querySelector("#file-queue-title"),
    fileQueueList: document.querySelector("#file-queue-list"),
    clearFiles: document.querySelector("#clear-files"),
    addMoreFiles: document.querySelector("#add-more-files"),
    uploadProgress: document.querySelector("#upload-progress"),
    progressLabel: document.querySelector("#progress-label"),
    progressValue: document.querySelector("#progress-value"),
    progressBar: document.querySelector("#progress-bar"),
    importMessage: document.querySelector("#import-message"),
    submitImport: document.querySelector("#submit-import"),
    deleteMemory: document.querySelector("#delete-memory"),
    deleteDialog: document.querySelector("#delete-dialog"),
    deleteTitle: document.querySelector("#delete-title"),
    closeDelete: document.querySelector("#close-delete"),
    cancelDelete: document.querySelector("#cancel-delete"),
    submitDelete: document.querySelector("#submit-delete"),
    toastRegion: document.querySelector("#toast-region"),
  };

  const openImportButtons = [
    document.querySelector("#open-import"),
    document.querySelector("#welcome-import"),
  ];

  async function requestJSON(path, method = "GET") {
    const response = await fetch(path, {
      method,
      headers: { Accept: "application/json" },
    });
    const payload = await response.json().catch(() => null);

    if (!response.ok) {
      const message = payload?.message || `Request failed (${response.status})`;
      throw new Error(message);
    }

    return payload;
  }

  function formatBytes(bytes) {
    if (!Number.isFinite(bytes)) return "Unknown size";
    if (bytes < 1024) return `${bytes} B`;

    const units = ["KiB", "MiB", "GiB"];
    let value = bytes / 1024;
    let unit = units[0];
    for (let index = 1; index < units.length && value >= 1024; index += 1) {
      value /= 1024;
      unit = units[index];
    }

    const digits = value >= 10 ? 0 : 1;
    return `${value.toFixed(digits)} ${unit}`;
  }

  function formatDate(value, style = "medium") {
    if (!value) return "Not available";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;

    return new Intl.DateTimeFormat(undefined, {
      dateStyle: style,
      ...(style === "long" ? { timeStyle: "short", hourCycle: "h23" } : {}),
    }).format(date);
  }

  function relativeDate(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "";

    const elapsedDays = Math.floor((Date.now() - date.getTime()) / 86_400_000);
    if (elapsedDays <= 0) return "Today";
    if (elapsedDays === 1) return "Yesterday";
    if (elapsedDays < 7) return `${elapsedDays}d ago`;

    return new Intl.DateTimeFormat(undefined, {
      month: "short",
      day: "numeric",
    }).format(date);
  }

  function mediaKind(mediaType, filename = "") {
    const normalized = (mediaType || "").toLowerCase();
    const extension = filename.split(".").pop()?.toUpperCase() || "FILE";
    if (normalized.includes("pdf")) return "PDF";
    if (normalized.startsWith("image/")) return "IMG";
    if (
      normalized.startsWith("text/") ||
      normalized.includes("json") ||
      normalized.includes("xml")
    )
      return "TXT";

    return extension.slice(0, 4);
  }

  function displayMediaType(mediaType) {
    if (!mediaType) return "Unknown format";
    const [family, subtype] = mediaType.split("/");
    if (!subtype) return mediaType;

    const friendlySubtype = subtype.split(";")[0].replaceAll(/[-+.]/g, " ");
    return family === "application"
      ? friendlySubtype.toUpperCase()
      : `${family} · ${friendlySubtype}`;
  }

  function createFileIcon(memory) {
    const icon = document.createElement("span");
    const kind = mediaKind(memory.media_type, memory.original_filename);
    icon.className = "file-icon";
    icon.dataset.kind = kind;
    icon.textContent = kind;
    icon.setAttribute("aria-hidden", "true");
    return icon;
  }

  function renderMemoryList() {
    elements.memoryList.replaceChildren();
    elements.memoryCount.textContent = state.memories.length;
    if (state.memories.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-list";
      empty.textContent = "Your Vault is quiet for now. Add a memory to begin.";
      elements.memoryList.append(empty);
    }

    for (const memory of state.memories) {
      const row = document.createElement("button");
      row.type = "button";
      row.className = "memory-row";
      row.dataset.memoryID = memory.id;
      row.classList.toggle("is-selected", memory.id === state.selectedID);
      row.setAttribute(
        "aria-pressed",
        memory.id === state.selectedID ? "true" : "false",
      );
      row.append(createFileIcon(memory));

      const copy = document.createElement("span");
      copy.className = "memory-row-copy";
      const name = document.createElement("strong");
      name.textContent = memory.original_filename;
      const metadata = document.createElement("span");
      metadata.textContent = `${displayMediaType(memory.media_type)} · ${formatBytes(memory.byte_size)}`;
      copy.append(name, metadata);

      const time = document.createElement("span");
      time.className = "memory-row-time";
      time.textContent = relativeDate(memory.imported_at);
      row.append(copy, time);
      row.addEventListener("click", () => selectMemory(memory.id));
      elements.memoryList.append(row);
    }

    elements.loadMore.hidden = !state.nextCursor;
  }


  function invalidateSearch() {
    state.searchGeneration += 1;
    state.searchLoading = false;
    state.searchLoadingMore = false;
    state.searchVisible = false;
    elements.searchLoadMore.disabled = false;
    elements.searchLoadMore.textContent = "Load more results";
    elements.searchView.hidden = true;
  }


  function searchResultStatus(response) {
    if (response.total === 0) return "No memories match this query.";
    return response.items.length < response.total
      ? `Showing ${response.items.length} of ${response.total}`
      : `${response.total} memories found.`;
  }

  function renderSearchResponse(
    response,
    { items = response.items || [], append = false } = {},
  ) {
    const plan = response.query_plan;
    const query = document.createElement("p");
    query.textContent = `Query: ${plan.query}`;
    const terms = document.createElement("p");
    terms.textContent = `Text terms: ${plan.terms.join(" AND ")}`;
    elements.searchPlan.replaceChildren(query, terms);

    if (!append) elements.searchResults.replaceChildren();
    for (const hit of items) {
      const memory = hit.memory;
      const row = document.createElement("button");
      row.type = "button";
      row.className = "search-result memory-row";
      row.dataset.memoryID = memory.id;
      row.append(createFileIcon(memory));

      const copy = document.createElement("span");
      copy.className = "memory-row-copy";
      const name = document.createElement("strong");
      const fragments = new RegExp(
        `(?=(${[...plan.terms]
          .sort((left, right) => right.length - left.length)
          .map((term) => term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
          .join("|")}))`,
        "giu",
      );
      let end = 0;
      for (const match of memory.original_filename.matchAll(fragments)) {
        const nextEnd = match.index + match[1].length;
        if (nextEnd <= end) continue;
        if (match.index < end) {
          name.lastChild.textContent += memory.original_filename.slice(end, nextEnd);
        } else {
          name.append(document.createTextNode(memory.original_filename.slice(end, match.index)));
          const marked = document.createElement("mark");
          marked.textContent = match[1];
          name.append(marked);
        }
        end = nextEnd;
      }
      name.append(document.createTextNode(memory.original_filename.slice(end)));
      const mediaType = document.createElement("span");
      mediaType.textContent = displayMediaType(memory.media_type);
      copy.append(name, mediaType);

      if (hit.excerpt?.length) {
        const excerpt = document.createElement("span");
        excerpt.className = "search-excerpt";
        for (const part of hit.excerpt) {
          const text = document.createElement(part.match ? "mark" : "span");
          text.textContent = part.text;
          excerpt.append(text);
        }
        copy.append(excerpt);
      }

      row.append(copy);
      row.addEventListener("click", () =>
        selectMemory(memory.id, { fromSearch: true }),
      );
      elements.searchResults.append(row);
    }

    elements.searchStatus.textContent = searchResultStatus(response);
    elements.searchError.hidden = true;
    elements.searchError.textContent = "";
    elements.searchLoadMore.hidden = !response.next_cursor;
    elements.searchLoadMore.disabled = state.searchLoadingMore;
    elements.searchView.hidden = false;
    state.searchVisible = true;
    elements.detailLoading.hidden = true;
    elements.memoryDetail.hidden = true;
    elements.welcomeState.hidden = true;
    elements.backToSearch.hidden = true;
  }

  async function startSearch(query) {
    invalidateSearch();
    stopUnderstandingRefresh();
    revokePreviewURL();
    state.selectedID = null;
    state.detailFromSearch = false;
    state.searchPhrase = query;
    state.searchResponse = null;
    state.searchScrollTop = 0;
    state.searchVisible = true;
    state.searchLoading = true;
    const generation = state.searchGeneration;

    elements.backToSearch.hidden = true;
    elements.searchPlan.replaceChildren();
    elements.searchStatus.textContent = "Searching…";
    elements.searchError.hidden = true;
    elements.searchError.textContent = "";
    elements.searchResults.replaceChildren();
    elements.searchResults.scrollTop = 0;
    elements.searchLoadMore.hidden = true;
    elements.searchView.hidden = false;
    elements.welcomeState.hidden = true;
    elements.memoryDetail.hidden = true;
    elements.detailLoading.hidden = true;
    if (window.innerWidth <= 680) document.body.classList.add("is-detail-open");

    const params = new URLSearchParams({ query, limit: "50" });
    try {
      const response = await requestJSON(
        `${apiBase}/memories/search?${params}`,
      );
      if (generation !== state.searchGeneration) return;
      state.searchResponse = response;
      state.searchLoading = false;
      renderSearchResponse(response);
    } catch (error) {
      if (generation !== state.searchGeneration) return;
      state.searchLoading = false;
      elements.searchStatus.textContent = "";
      elements.searchError.textContent = `Could not search memories. ${error.message}`;
      elements.searchError.hidden = false;
    }
  }

  async function loadMoreSearchResults() {
    const cursor = state.searchResponse?.next_cursor;
    if (!cursor || state.searchLoadingMore || state.searchLoading) return;
    const generation = state.searchGeneration;
    const query = state.searchResponse.query_plan.query;
    state.searchLoadingMore = true;
    elements.searchLoadMore.disabled = true;
    elements.searchLoadMore.textContent = "Loading…";
    elements.searchStatus.textContent = "Loading more results…";
    elements.searchError.hidden = true;
    elements.searchError.textContent = "";

    const params = new URLSearchParams({ query, limit: "50", cursor });
    try {
      const page = await requestJSON(
        `${apiBase}/memories/search?${params}`,
      );
      if (generation !== state.searchGeneration || !state.searchVisible) return;
      if (page.next_cursor === cursor) {
        throw new Error("The server repeated its search continuation.");
      }
      if (page.next_cursor && !(page.items || []).length) {
        throw new Error("The server returned an empty page with more results.");
      }
      const existing = new Set(
        state.searchResponse.items.map((hit) => hit.memory.id),
      );
      const items = (page.items || []).filter((hit) => {
        if (existing.has(hit.memory.id)) return false;
        existing.add(hit.memory.id);
        return true;
      });
      state.searchResponse = {
        ...state.searchResponse,
        total: page.total,
        items: [...state.searchResponse.items, ...items],
        next_cursor: page.next_cursor || null,
      };
      state.searchLoadingMore = false;
      renderSearchResponse(state.searchResponse, { items, append: true });
    } catch (error) {
      if (generation !== state.searchGeneration || !state.searchVisible) return;
      elements.searchStatus.textContent = searchResultStatus(state.searchResponse);
      elements.searchError.textContent = `Could not load more results. ${error.message}`;
      elements.searchError.hidden = false;
    } finally {
      if (generation === state.searchGeneration) {
        state.searchLoadingMore = false;
        elements.searchLoadMore.disabled = false;
        elements.searchLoadMore.textContent = "Load more results";
      }
    }
  }

  function returnToSearchResults() {
    stopUnderstandingRefresh();
    invalidateSearch();
    revokePreviewURL();
    state.selectedID = null;
    state.detailFromSearch = false;
    renderMemoryList();
    if (state.searchResponse) {
      elements.searchQuery.value = state.searchPhrase;
      renderSearchResponse(state.searchResponse);
      elements.searchResults.scrollTop = state.searchScrollTop;
    } else {
      renderNoSelectionState();
    }
  }

  function clearSearch() {
    if (
      !state.searchResponse &&
      !state.searchVisible &&
      !state.searchLoading &&
      !state.detailFromSearch
    )
      return;
    const keepBrowseSelection =
      state.selectedID !== null && !state.detailFromSearch;
    invalidateSearch();
    state.searchResponse = null;
    state.searchPhrase = "";
    state.searchScrollTop = 0;
    elements.searchPlan.replaceChildren();
    elements.searchStatus.textContent = "";
    elements.searchError.textContent = "";
    elements.searchError.hidden = true;
    elements.searchResults.replaceChildren();
    elements.searchLoadMore.hidden = true;
    if (keepBrowseSelection) return;

    stopUnderstandingRefresh();
    revokePreviewURL();
    state.selectedID = null;
    state.detailFromSearch = false;
    renderMemoryList();
    document.body.classList.remove("is-detail-open");
    renderNoSelectionState();
  }

  function renderNoSelectionState() {
    state.searchVisible = false;
    elements.searchView.hidden = true;
    elements.backToSearch.hidden = true;
    state.detailFromSearch = false;
    stopUnderstandingRefresh();
    const hasMemories = state.memories.length > 0;

    if (hasMemories) document.body.classList.remove("is-detail-open");

    elements.welcomeEyebrow.textContent = hasMemories
      ? "Your memory vault"
      : "Personal memory vault";
    elements.welcomeTitle.textContent = hasMemories
      ? "Choose a memory to revisit."
      : "Your memory, kept close.";
    elements.welcomeCopy.textContent = hasMemories
      ? "Select a memory from the list to view its preview and provenance, or import another memory."
      : "Import the things worth keeping. memoryd preserves the original and makes its provenance easy to inspect.";
    elements.welcomeImport.textContent = hasMemories
      ? "Import another memory"
      : "Add your first memory";
    elements.welcomeNote.textContent = hasMemories
      ? "Stored locally · Select a memory to inspect it"
      : "Stored locally · Original bytes preserved";
    elements.detailLoading.hidden = true;
    elements.memoryDetail.hidden = true;
    elements.welcomeState.hidden = false;
  }

  async function loadMemories({ append = false } = {}) {
    if (append && state.loadingMore) return;
    state.loadingMore = append;
    elements.loadMore.disabled = append;
    elements.loadMore.textContent = append ? "Loading…" : "Load older memories";

    try {
      const cursor =
        append && state.nextCursor
          ? `?limit=50&cursor=${encodeURIComponent(state.nextCursor)}`
          : "?limit=50";
      const page = await requestJSON(`${apiBase}/memories${cursor}`);
      state.memories = append ? [...state.memories, ...page.items] : page.items;
      state.nextCursor = page.next_cursor || null;
      renderMemoryList();

      if (
        !append &&
        state.selectedID === null &&
        !state.searchVisible &&
        !state.searchLoading
      ) {
        renderNoSelectionState();
        if (state.memories.length && window.innerWidth > 680) {
          await selectMemory(state.memories[0].id, { openMobile: false });
        }
      }
    } catch (error) {
      if (!append) {
        elements.memoryList.replaceChildren();
        const message = document.createElement("div");
        message.className = "error-list";
        message.textContent = `Could not open the Vault. ${error.message}`;
        elements.memoryList.append(message);
      } else {
        showToast(`Could not load more memories. ${error.message}`);
      }
    } finally {
      state.loadingMore = false;
      elements.loadMore.disabled = false;
      elements.loadMore.textContent = "Load older memories";
    }
  }

  function setDetailLoading(loading) {
    elements.detailLoading.hidden = !loading;
    if (loading) {
      elements.welcomeState.hidden = true;
      elements.memoryDetail.hidden = true;
    }
  }

  function selectDetailTab(tab) {
    const original = tab === "original";
    elements.originalPanel.hidden = !original;
    elements.understandingPanel.hidden = original;
    elements.originalTab.setAttribute("aria-pressed", String(original));
    elements.understandingTab.setAttribute("aria-pressed", String(!original));
  }

  function setRebuildDisabled(disabled) {
    elements.refreshUnderstanding.disabled = disabled;
    elements.rebuildNoteToggle.disabled = disabled;
    elements.rebuildNoteSubmit.disabled = disabled;
  }

  function stopUnderstandingRefresh() {
    clearTimeout(state.refreshTimer);
    state.refreshTimer = null;
    state.detailGeneration += 1;
    state.refreshing = false;
    setRebuildDisabled(false);
    elements.rebuildNotePopover.hidePopover();
    elements.refreshUnderstanding.textContent = "Rebuild";
    elements.refreshUnderstanding.setAttribute("aria-busy", "false");
  }

  function detailIsVisible() {
    return (
      !elements.memoryDetail.hidden &&
      elements.memoryDetail.getClientRects().length > 0
    );
  }

  function scheduleUnderstandingRefresh() {
    clearTimeout(state.refreshTimer);
    state.refreshTimer = null;
    if (
      state.refreshing ||
      document.hidden ||
      !detailIsVisible() ||
      (!state.understandingReload &&
        !["queued", "running"].includes(state.understanding?.status))
    )
      return;
    // ponytail: fixed cadence; add backoff only if polling load becomes material.
    state.refreshTimer = setTimeout(refreshUnderstanding, 3000);
  }

  async function refreshUnderstanding({ rerun = false, userNote } = {}) {
    clearTimeout(state.refreshTimer);
    state.refreshTimer = null;
    if (state.refreshing || !state.selectedID || !detailIsVisible()) return;
    if (rerun && ["queued", "running"].includes(state.understanding?.status))
      return;
    const memoryID = state.selectedID;
    const generation = state.detailGeneration;
    state.refreshing = true;
    setRebuildDisabled(true);
    elements.refreshUnderstanding.textContent = rerun
      ? "Starting…"
      : "Checking…";
    elements.refreshUnderstanding.setAttribute("aria-busy", "true");
    let started = false;
    const current = () =>
      state.selectedID === memoryID &&
      state.detailGeneration === generation &&
      detailIsVisible();
    try {
      let snapshot;
      if (rerun) {
        const response = await fetch(
          `${apiBase}/memories/${memoryID}/rebuild`,
          {
            method: "POST",
            headers: {
              Accept: "application/json",
              "Content-Type": "application/json",
            },
            body: JSON.stringify(
              userNote === undefined ? {} : { user_note: userNote },
            ),
          },
        );
        const payload = await response.json();
        if (response.status !== 202 && response.status !== 409) {
          throw new Error(
            payload.message || `Request failed (${response.status})`,
          );
        }
        started = true;
        if (!current()) return;
        snapshot = payload;
        if (response.status === 202 && userNote !== undefined) {
          state.understanding = { ...state.understanding, user_note: userNote };
        }
        if (userNote !== undefined) elements.rebuildNotePopover.hidePopover();
        showToast(
          response.status === 409
            ? "Understanding already pending."
            : "Understanding accepted.",
        );
      } else if (
        !state.understandingReload &&
        state.understanding?.status_url
      ) {
        const response = await fetch(state.understanding.status_url, {
          headers: { Accept: "application/json" },
          cache: "no-store",
        });
        if (!current()) return;
        if (response.status === 404) {
          state.understanding = { ...state.understanding, status_url: null };
          state.understandingReload = true;
        } else {
          const payload = await response.json();
          if (!response.ok)
            throw new Error(
              payload.message || `Request failed (${response.status})`,
            );
          snapshot = payload;
        }
      } else {
        state.understandingReload = true;
      }
      if (snapshot) {
        state.understanding = {
          ...state.understanding,
          status: snapshot.attempt.status,
          latest_attempt: snapshot.attempt,
          status_url: snapshot.status_url,
        };
        state.understandingReload = snapshot.attempt.status === "done";
        renderUnderstanding(state.understanding);
      }
      if (state.understandingReload) {
        const detail = await requestJSON(`${apiBase}/memories/${memoryID}`);
        if (!current()) return;
        state.understanding = detail.understanding;
        state.understandingReload = false;
        renderUnderstanding(state.understanding);
      }
      elements.understandingRefreshError.hidden = true;
      elements.understandingRefreshError.textContent = "";
    } catch (error) {
      if (
        state.selectedID !== memoryID ||
        state.detailGeneration !== generation ||
        !detailIsVisible()
      )
        return;
      elements.understandingRefreshError.textContent = `${rerun && !started ? "Could not start understanding." : "Could not refresh understanding."} ${error.message}`;
      elements.understandingRefreshError.hidden = false;
    } finally {
      if (
        state.selectedID === memoryID &&
        state.detailGeneration === generation
      ) {
        state.refreshing = false;
        setRebuildDisabled(
          ["queued", "running"].includes(state.understanding?.status),
        );
        elements.refreshUnderstanding.textContent = "Rebuild";
        elements.refreshUnderstanding.setAttribute("aria-busy", "false");
        scheduleUnderstandingRefresh();
      }
    }
  }

  async function selectMemory(
    memoryID,
    { openMobile = true, fromSearch = false } = {},
  ) {
    // ponytail: obsolete requests finish; generation guards discard them without cancellation.
    if (fromSearch) {
      state.searchScrollTop = elements.searchResults.scrollTop;
    }
    invalidateSearch();
    stopUnderstandingRefresh();
    const generation = state.detailGeneration;
    let loaded = false;
    state.selectedID = memoryID;
    state.detailFromSearch = fromSearch;
    elements.searchView.hidden = true;
    elements.backToSearch.hidden = !fromSearch;
    selectDetailTab("original");
    state.understanding = null;
    state.understandingReload = false;
    state.displayedRunID = null;
    elements.understandingArtifacts.replaceChildren();
    state.understandingSection = null;
    elements.understandingArtifactSections.replaceChildren();
    elements.understandingRefreshError.hidden = true;
    elements.understandingRefreshError.textContent = "";
    renderMemoryList();
    setDetailLoading(true);
    if (openMobile) document.body.classList.add("is-detail-open");

    try {
      const detail = await requestJSON(`${apiBase}/memories/${memoryID}`);
      if (
        state.selectedID !== memoryID ||
        state.detailGeneration !== generation
      )
        return;
      renderDetail(detail);
      loaded = true;
    } catch (error) {
      if (
        state.selectedID !== memoryID ||
        state.detailGeneration !== generation
      )
        return;
      showToast(`Could not open memory. ${error.message}`);
      state.selectedID = null;
      renderMemoryList();
      if (fromSearch && state.searchResponse) {
        state.detailFromSearch = false;
        revokePreviewURL();
        stopUnderstandingRefresh();
        setDetailLoading(false);
        elements.searchQuery.value = state.searchPhrase;
        renderSearchResponse(state.searchResponse);
        elements.searchResults.scrollTop = state.searchScrollTop;
      } else {
        state.detailFromSearch = false;
        renderNoSelectionState();
      }
    } finally {
      if (
        state.selectedID === memoryID &&
        state.detailGeneration === generation
      ) {
        setDetailLoading(false);
        if (loaded) scheduleUnderstandingRefresh();
      }
    }
  }

  function addPill(text) {
    const pill = document.createElement("span");
    pill.className = "detail-pill";
    pill.textContent = text;
    elements.detailPills.append(pill);
  }

  function addMetadata(label, value) {
    if (value === undefined || value === null || value === "") return;
    const row = document.createElement("div");
    row.className = "metadata-row";
    const term = document.createElement("dt");
    term.textContent = label;
    const description = document.createElement("dd");
    description.textContent = value;
    row.append(term, description);
    elements.metadataList.append(row);
  }

  function jsonDetails(label, value) {
    const details = document.createElement("details");
    const summary = document.createElement("summary");
    summary.textContent = label;
    const pre = document.createElement("pre");
    pre.textContent = JSON.stringify(value, null, 2);
    details.append(summary, pre);
    return details;
  }

  function selectUnderstandingSection(section) {
    const buttons = [
      ...elements.understandingSections.querySelectorAll("button"),
    ].filter((button) => !button.hidden);
    const selected =
      buttons.find(
        (button) => button.dataset.understandingSection === section,
      ) || buttons[0];
    state.understandingSection = selected?.dataset.understandingSection || null;
    for (const button of buttons) {
      button.setAttribute("aria-pressed", String(button === selected));
    }
    for (const pane of elements.understandingPanel.querySelectorAll(
      "section[data-understanding-section]",
    )) {
      pane.hidden =
        pane.dataset.understandingSection !== state.understandingSection;
    }
  }

  function renderUnderstanding(understanding) {
    const status = understanding?.status;
    elements.understandingStatus.textContent =
      {
        not_started: "Understanding not started",
        queued: "Understanding queued",
        running: "Understanding running",
        done: "Understanding done",
        failed: "Understanding failed",
      }[status] || "Understanding unavailable";
    setRebuildDisabled(
      state.refreshing || ["queued", "running"].includes(status),
    );
    elements.understandingUserNote.textContent =
      understanding?.user_note || "No user note saved.";
    elements.understandingSections.querySelector(
      '[data-understanding-section="saved-note"]',
    ).hidden = !understanding?.user_note;
    elements.understandingAttempt.replaceChildren();
    const attempt = understanding?.latest_attempt;
    if (status === "failed") {
      const error = document.createElement("p");
      error.textContent =
        attempt?.diagnostics?.error || "Understanding failed.";
      elements.understandingAttempt.append(error);
    }
    const diagnostics = status === "failed" ? attempt?.diagnostics : null;
    elements.understandingDiagnostics.querySelector("pre").textContent =
      diagnostics == null ? "" : JSON.stringify(diagnostics, null, 2);
    elements.understandingSections.querySelector(
      '[data-understanding-section="diagnostics"]',
    ).hidden = diagnostics == null;

    const run = understanding?.active_run;
    elements.understandingArtifactHeading.hidden = !run?.artifacts.length;
    elements.understandingCompletedAt.textContent = formatDate(
      run?.completed_at,
      "long",
    );
    elements.understandingRunSummary.replaceChildren();
    elements.understandingSections.querySelector(
      '[data-understanding-section="run-details"]',
    ).hidden = !run;
    if (!run) {
      elements.understandingRunSummary.textContent =
        "No completed Understanding Run yet.";
      elements.understandingArtifacts.replaceChildren();
      elements.understandingArtifactSections.replaceChildren();
      state.displayedRunID = null;
      selectUnderstandingSection(state.understandingSection);
      return;
    }

    if (run.warnings.length) {
      const notice = document.createElement("section");
      notice.className = "understanding-warnings";
      const heading = document.createElement("h3");
      heading.textContent = "Warnings";
      const list = document.createElement("ul");
      for (const warning of run.warnings) {
        const message = document.createElement("li");
        message.textContent = warning;
        list.append(message);
      }
      notice.append(heading, list);
      elements.understandingRunSummary.append(notice);
    }

    const { artifacts, warnings, ...report } = run;
    if (attempt) {
      const { diagnostics, ...identity } = attempt;
      report.latest_attempt = identity;
    }
    elements.understandingRunDetails.querySelector("pre").textContent =
      JSON.stringify(report, null, 2);

    // Runs are immutable; preserve content and section selection on status refresh.
    if (state.displayedRunID === run.id) {
      selectUnderstandingSection(state.understandingSection);
      return;
    }
    state.displayedRunID = run.id;
    elements.understandingArtifacts.replaceChildren();
    elements.understandingArtifactSections.replaceChildren();
    for (const [index, artifact] of artifacts.entries()) {
      const section = `artifact-${index}`;
      const card = document.createElement("section");
      card.id = `understanding-artifact-${index}`;
      card.dataset.understandingSection = section;
      const label = `Artifact ${index + 1} · ${artifact.content_type}`;
      const heading = document.createElement("h3");
      heading.textContent = label;
      card.append(heading);
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = label;
      button.dataset.understandingSection = section;
      button.setAttribute("aria-controls", card.id);
      button.setAttribute("aria-pressed", "false");
      elements.understandingArtifactSections.append(button);
      const type = artifact.content_type.split(";")[0].trim().toLowerCase();
      if (type === "text/markdown") {
        card.append(renderMarkdown(artifact.content));
        const source = document.createElement("details");
        const sourceLabel = document.createElement("summary");
        sourceLabel.textContent = "View source";
        const pre = document.createElement("pre");
        pre.textContent = artifact.content;
        source.append(sourceLabel, pre);
        card.append(source);
      } else {
        const pre = document.createElement("pre");
        pre.textContent = artifact.content;
        card.append(pre);
      }
      const metadata = {
        id: artifact.id,
        blob_hash: artifact.blob_hash,
        byte_size: artifact.byte_size,
      };
      for (const key of ["provenance", "scope"]) {
        if (Object.prototype.hasOwnProperty.call(artifact, key)) {
          metadata[key] = artifact[key];
        }
      }
      card.append(jsonDetails("Metadata", metadata));
      elements.understandingArtifacts.append(card);
    }
    selectUnderstandingSection(null);
  }

  function renderDetail(detail) {
    const memory = detail.memory;
    const context = detail.import_context || {};
    const contentURL =
      detail.content_url || `${apiBase}/memories/${memory.id}/content`;

    elements.welcomeState.hidden = true;
    elements.memoryDetail.hidden = false;
    elements.detailTitle.textContent = memory.original_filename;
    elements.detailPills.replaceChildren();
    addPill(displayMediaType(memory.media_type));
    addPill(formatBytes(memory.byte_size));
    addPill(`Imported ${formatDate(memory.imported_at)}`);
    elements.downloadMemory.href = contentURL;
    elements.downloadMemory.setAttribute("download", memory.original_filename);
    elements.detailMemoryId.textContent = memory.id;
    elements.detailMemoryId.dataset.value = memory.id;
    elements.detailHash.textContent = memory.blob_hash;
    elements.copyHash.dataset.hash = memory.blob_hash;

    elements.metadataList.replaceChildren();
    addMetadata("Original filename", context.original_filename);
    addMetadata("Media type", memory.media_type);
    addMetadata("Byte size", formatBytes(memory.byte_size));
    addMetadata("Imported", formatDate(memory.imported_at, "long"));
    addMetadata(
      "Original created",
      formatDate(context.filesystem_created_at, "long"),
    );
    addMetadata(
      "Original modified",
      formatDate(context.filesystem_modified_at, "long"),
    );
    addMetadata("Relative path", context.relative_path);
    addMetadata("Full path", context.full_path);

    state.understanding = detail.understanding;
    renderUnderstanding(state.understanding);
    renderPreview(memory, contentURL);
  }

  function revokePreviewURL() {
    if (!state.previewURL) return;
    URL.revokeObjectURL(state.previewURL);
    state.previewURL = null;
  }

  function renderPreviewPlaceholder(memory, message) {
    elements.previewStage.replaceChildren();
    const placeholder = document.createElement("div");
    placeholder.className = "preview-placeholder";
    placeholder.append(createFileIcon(memory));
    const title = document.createElement("strong");
    title.textContent = "Preview unavailable";
    const copy = document.createElement("span");
    copy.textContent = message;
    placeholder.append(title, copy);
    elements.previewStage.append(placeholder);
  }

  async function renderPreview(memory, contentURL) {
    const generation = state.detailGeneration;
    revokePreviewURL();
    elements.previewStage.replaceChildren();

    if (memory.byte_size > maxAutomaticPreviewBytes) {
      renderPreviewPlaceholder(
        memory,
        "This Blob is large, so it is available to download without loading a preview.",
      );
      return;
    }

    const mediaType = (memory.media_type || "").toLowerCase().split(";")[0];
    const canPreview =
      mediaType === "application/pdf" ||
      mediaType.startsWith("image/") ||
      mediaType.startsWith("text/") ||
      mediaType.includes("json") ||
      mediaType.includes("xml");
    if (!canPreview) {
      renderPreviewPlaceholder(
        memory,
        "This format is preserved intact and ready to download.",
      );
      return;
    }

    const spinner = document.createElement("span");
    spinner.className = "preview-spinner";
    spinner.setAttribute("aria-label", "Loading preview");
    elements.previewStage.append(spinner);

    try {
      const response = await fetch(contentURL);
      if (
        state.selectedID !== memory.id ||
        state.detailGeneration !== generation
      )
        return;
      if (!response.ok) throw new Error(`Download failed (${response.status})`);
      const blob = await response.blob();
      if (
        state.selectedID !== memory.id ||
        state.detailGeneration !== generation
      )
        return;

      if (
        mediaType.startsWith("text/") ||
        mediaType.includes("json") ||
        mediaType.includes("xml")
      ) {
        const text = await blob.text();
        if (
          state.selectedID !== memory.id ||
          state.detailGeneration !== generation
        )
          return;
        elements.previewStage.replaceChildren();
        if (mediaType === "text/markdown") {
          const rendered = renderMarkdown(text);
          rendered.classList.add("text-preview");
          elements.previewStage.append(rendered);
          return;
        }
        const pre = document.createElement("pre");
        pre.className = "text-preview";
        pre.textContent = text;
        elements.previewStage.append(pre);
        return;
      }

      elements.previewStage.replaceChildren();
      state.previewURL = URL.createObjectURL(blob);
      if (mediaType.startsWith("image/")) {
        const image = document.createElement("img");
        image.src = state.previewURL;
        image.alt = `Preview of ${memory.original_filename}`;
        elements.previewStage.append(image);
      } else {
        const frame = document.createElement("iframe");
        frame.src = state.previewURL;
        frame.title = `Preview of ${memory.original_filename}`;
        elements.previewStage.append(frame);
      }
    } catch (error) {
      if (
        state.selectedID !== memory.id ||
        state.detailGeneration !== generation
      )
        return;
      renderPreviewPlaceholder(
        memory,
        `The original is still available to download. ${error.message}`,
      );
    }
  }

  async function checkHealth() {
    try {
      const health = await requestJSON(`${apiBase}/readyz`);
      elements.vaultStatus.classList.add("is-ready");
      elements.vaultStatusLabel.textContent = "Vault ready";
      if (health.vault_path) elements.vaultStatus.title = health.vault_path;
    } catch {
      elements.vaultStatus.classList.add("is-offline");
      elements.vaultStatusLabel.textContent = "Vault unavailable";
    }
  }

  function showImport() {
    resetImportForm();
    elements.importDialog.showModal();
  }

  function addSelectedFiles(files) {
    if (state.importing || state.importComplete) return;

    elements.importMessage.hidden = true;
    elements.importMessage.replaceChildren();

    for (const file of files) {
      state.selectedFiles.push({
        id: state.nextQueueID,
        file,
        status: file.size > maxBlobBytes ? "oversized" : "ready",
        message: file.size > maxBlobBytes ? "Exceeds the 100 MiB limit" : "",
        existingID: null,
      });
      state.nextQueueID += 1;
    }
    elements.fileInput.value = "";
    renderFileQueue();
  }

  function removeSelectedFile(itemID) {
    if (state.importing) return;
    state.selectedFiles = state.selectedFiles.filter(
      (item) => item.id !== itemID,
    );
    renderFileQueue();
  }

  function queueStatus(item) {
    switch (item.status) {
      case "uploading":
        return "Uploading";
      case "success":
        return "Added";
      case "duplicate":
        return "Already stored";
      case "error":
        return "Failed";
      case "oversized":
        return "Too large";
      default:
        return "Ready";
    }
  }

  function renderFileQueue() {
    const count = state.selectedFiles.length;
    elements.dropZone.hidden = count > 0;
    elements.fileQueue.hidden = count === 0;
    elements.fileQueueTitle.textContent = `${count} ${count === 1 ? "file" : "files"} selected`;
    elements.fileQueueList.replaceChildren();

    for (const item of state.selectedFiles) {
      const row = document.createElement("div");
      row.className = "file-queue-row";

      const glyph = document.createElement("div");
      glyph.className = "file-glyph";
      glyph.textContent = mediaKind(item.file.type, item.file.name);

      const copy = document.createElement("div");
      copy.className = "file-queue-copy";
      const name = document.createElement("strong");
      name.textContent = item.file.name;
      const metadata = document.createElement("span");
      metadata.textContent = `${displayMediaType(item.file.type)} · ${formatBytes(item.file.size)}`;
      copy.append(name, metadata);

      let action;
      if (item.status === "ready" && !state.importing) {
        action = document.createElement("button");
        action.type = "button";
        action.className = "file-queue-action";
        action.textContent = "Remove";
        action.addEventListener("click", () => removeSelectedFile(item.id));
      } else if (
        item.status === "duplicate" &&
        item.existingID &&
        !state.importing
      ) {
        action = document.createElement("button");
        action.type = "button";
        action.className = "file-queue-action";
        action.textContent = "Open existing";
        action.addEventListener("click", () => {
          elements.importDialog.close();
          selectMemory(item.existingID);
        });
      } else {
        action = document.createElement("span");
        action.className = "file-queue-status";
        action.dataset.status = item.status;
        action.textContent = queueStatus(item);
        if (item.message) action.title = item.message;
      }
      row.append(glyph, copy, action);
      elements.fileQueueList.append(row);
    }

    const readyCount = state.selectedFiles.filter(
      (item) => item.status === "ready",
    ).length;
    elements.clearFiles.disabled = state.importing;
    elements.addMoreFiles.hidden = state.importing || state.importComplete;
    elements.submitImport.disabled =
      state.importing || (!state.importComplete && readyCount === 0);
    if (state.importComplete) {
      elements.submitImport.textContent = "Done";
    } else if (readyCount > 0) {
      elements.submitImport.textContent = `Add ${readyCount} to vault`;
    } else {
      elements.submitImport.textContent = "Add to vault";
    }
  }

  function resetImportForm() {
    state.selectedFiles = [];
    state.importing = false;
    state.importComplete = false;
    elements.closeImport.disabled = false;
    elements.fileInput.value = "";
    elements.dropZone.hidden = false;
    elements.fileQueue.hidden = true;
    elements.fileQueueList.replaceChildren();
    elements.uploadProgress.hidden = true;
    elements.progressBar.style.width = "0%";
    elements.progressValue.textContent = "0%";
    elements.progressLabel.textContent = "Uploading…";
    elements.importMessage.hidden = true;
    elements.importMessage.classList.remove("is-duplicate");
    elements.importMessage.replaceChildren();
    elements.submitImport.disabled = true;
    elements.submitImport.textContent = "Add to vault";
  }

  function showImportMessage(message) {
    elements.importMessage.classList.remove("is-duplicate");
    elements.importMessage.textContent = message;
    elements.importMessage.hidden = false;
  }

  function uploadMemory(file, onProgress) {
    const body = new FormData();
    body.append("file", file, file.name);
    if (file.lastModified) {
      const context = new Blob(
        [
          JSON.stringify({
            filesystem_modified_at: new Date(file.lastModified).toISOString(),
          }),
        ],
        { type: "application/json" },
      );
      body.append("import_context", context);
    }

    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest();
      request.open("POST", `${apiBase}/memories/import`);
      request.setRequestHeader("Accept", "application/json");
      request.upload.addEventListener("progress", (event) => {
        if (!event.lengthComputable) return;
        const percent = Math.min(
          100,
          Math.round((event.loaded / event.total) * 100),
        );
        onProgress(percent);
      });
      request.addEventListener("load", () => {
        let payload = null;
        try {
          payload = JSON.parse(request.responseText);
        } catch {
          payload = null;
        }

        if (request.status >= 200 && request.status < 300) {
          resolve(payload);
        } else {
          reject({
            status: request.status,
            payload,
            message: payload?.message || `Import failed (${request.status})`,
          });
        }
      });
      request.addEventListener("error", () => {
        reject({
          status: 0,
          payload: null,
          message: "Could not reach the Vault.",
        });
      });
      request.send(body);
    });
  }

  async function handleImport(event) {
    event.preventDefault();
    if (state.importComplete) {
      elements.importDialog.close();
      return;
    }

    const readyItems = state.selectedFiles.filter(
      (item) => item.status === "ready",
    );
    if (readyItems.length === 0 || state.importing) return;

    state.importing = true;
    elements.closeImport.disabled = true;
    elements.uploadProgress.hidden = false;
    elements.importMessage.hidden = true;
    renderFileQueue();
    const imported = [];

    for (let index = 0; index < readyItems.length; index += 1) {
      const item = readyItems[index];
      item.status = "uploading";
      elements.progressBar.style.width = "0%";
      elements.progressValue.textContent = "0%";
      elements.progressLabel.textContent = `Uploading ${index + 1} of ${readyItems.length} · ${item.file.name}`;
      renderFileQueue();

      try {
        const memory = await uploadMemory(item.file, (percent) => {
          elements.progressBar.style.width = `${percent}%`;
          elements.progressValue.textContent = `${percent}%`;
          if (percent === 100) {
            elements.progressLabel.textContent = `Committing ${item.file.name}…`;
          }
        });
        item.status = "success";
        imported.push(memory);
        state.memories = [
          memory,
          ...state.memories.filter((entry) => entry.id !== memory.id),
        ];
      } catch (error) {
        if (error.status === 409) {
          item.status = "duplicate";
          item.message = error.message;
          item.existingID = error.payload?.existing_memory?.id || null;
        } else {
          item.status = "error";
          item.message = error.message;
        }
      }
      renderFileQueue();
    }

    state.importing = false;
    state.importComplete = true;
    elements.closeImport.disabled = false;
    elements.uploadProgress.hidden = true;
    renderMemoryList();
    renderFileQueue();

    const duplicates = state.selectedFiles.filter(
      (item) => item.status === "duplicate",
    ).length;
    const failed = state.selectedFiles.filter((item) => {
      return item.status === "error" || item.status === "oversized";
    }).length;
    if (duplicates === 0 && failed === 0) {
      const noun = imported.length === 1 ? "memory" : "memories";
      elements.importDialog.close();
      showToast(`${imported.length} ${noun} added to the Vault.`);
      await selectMemory(imported[imported.length - 1].id);
      return;
    }

    const summary = [
      `${imported.length} added`,
      duplicates ? `${duplicates} already stored` : null,
      failed ? `${failed} failed` : null,
    ]
      .filter(Boolean)
      .join(" · ");
    showImportMessage(summary);
  }

  function showToast(message) {
    const toast = document.createElement("div");
    toast.className = "toast";
    toast.textContent = message;
    elements.toastRegion.append(toast);
    window.setTimeout(() => toast.remove(), 4200);
  }

  function showDeleteDialog() {
    const memory =
      state.memories.find((entry) => entry.id === state.selectedID) ||
      state.searchResponse?.items.find(
        (hit) => hit.memory.id === state.selectedID,
      )?.memory;
    elements.deleteTitle.textContent =
      memory?.original_filename || "This memory";
    elements.deleteDialog.showModal();
  }

  async function deleteSelectedMemory() {
    const memoryID = state.selectedID;
    if (!memoryID) return;
    const deletedFromSearch = state.detailFromSearch;
    const searchQuery = state.searchPhrase;
    elements.deleteDialog.close();
    elements.deleteMemory.disabled = true;
    elements.deleteMemory.textContent = "Deleting…";
    try {
      const response = await fetch(`${apiBase}/memories/${memoryID}`, {
        method: "DELETE",
        headers: { Accept: "application/json" },
      });
      if (response.status !== 204) {
        let message = `Delete failed (HTTP ${response.status}).`;
        try {
          const problem = await response.json();
          if (problem.message) message = problem.message;
        } catch {
          // keep the generic message
        }
        throw new Error(message);
      }
      state.memories = state.memories.filter((entry) => entry.id !== memoryID);
      const deletedMemoryWasSelected = state.selectedID === memoryID;
      if (deletedMemoryWasSelected) {
        stopUnderstandingRefresh();
        state.selectedID = null;
        revokePreviewURL();
      }
      renderMemoryList();
      showToast("Memory deleted.");
      if (deletedMemoryWasSelected) {
        if (deletedFromSearch && searchQuery !== undefined) {
          elements.searchQuery.value = searchQuery;
          await startSearch(searchQuery);
        } else {
          renderNoSelectionState();
        }
      }
    } catch (error) {
      showToast(error.message || "Could not delete the memory.");
    } finally {
      elements.deleteMemory.disabled = false;
      elements.deleteMemory.textContent = "Delete";
    }
  }

  for (const button of openImportButtons) {
    button.addEventListener("click", showImport);
  }
  elements.originalTab.addEventListener("click", () =>
    selectDetailTab("original"),
  );
  elements.understandingTab.addEventListener("click", () =>
    selectDetailTab("understanding"),
  );
  elements.refreshUnderstanding.addEventListener("click", () =>
    refreshUnderstanding({ rerun: true }),
  );
  elements.rebuildNoteForm.addEventListener("submit", (event) => {
    event.preventDefault();
    if (elements.rebuildNoteSubmit.disabled) return;
    refreshUnderstanding({
      rerun: true,
      userNote: elements.rebuildUserNote.value,
    });
  });
  elements.rebuildNotePopover.addEventListener("beforetoggle", (event) => {
    if (event.newState === "open") {
      elements.rebuildUserNote.value = state.understanding?.user_note || "";
    }
  });
  elements.rebuildNotePopover.addEventListener("toggle", (event) => {
    const open = event.newState === "open";
    elements.rebuildNoteToggle.setAttribute("aria-expanded", String(open));
    if (!open) return;
    const anchor = elements.rebuildNoteToggle.getBoundingClientRect();
    const popover = elements.rebuildNotePopover.getBoundingClientRect();
    elements.rebuildNotePopover.style.left = `${Math.max(12, Math.min(anchor.right - popover.width, window.innerWidth - popover.width - 12))}px`;
    elements.rebuildNotePopover.style.top = `${Math.max(12, Math.min(anchor.bottom + 8, window.innerHeight - popover.height - 12))}px`;
    elements.rebuildUserNote.focus();
  });
  window.addEventListener("resize", () =>
    elements.rebuildNotePopover.hidePopover(),
  );
  elements.understandingSections.addEventListener("click", (event) => {
    const button = event.target.closest("button[data-understanding-section]");
    if (!button) return;
    selectUnderstandingSection(button.dataset.understandingSection);
    elements.understandingPanel.querySelector(
      ".understanding-reading",
    ).scrollTop = 0;
  });
  elements.searchForm.addEventListener("submit", (event) => {
    event.preventDefault();
    if (!state.searchLoading) startSearch(elements.searchQuery.value);
  });
  elements.searchQuery.addEventListener("input", () => {
    if (elements.searchQuery.value.trim() === "") clearSearch();
  });
  elements.backToSearch.addEventListener("click", returnToSearchResults);
  elements.searchLoadMore.addEventListener("click", loadMoreSearchResults);
  elements.loadMore.addEventListener("click", () =>
    loadMemories({ append: true }),
  );
  elements.backToList.addEventListener("click", () => {
    invalidateSearch();
    stopUnderstandingRefresh();
    if (state.detailFromSearch) {
      state.selectedID = null;
      revokePreviewURL();
      renderMemoryList();
    }
    state.detailFromSearch = false;
    elements.backToSearch.hidden = true;
    document.body.classList.remove("is-detail-open");
  });
  elements.copyMemoryId.addEventListener("click", async () => {
    const mid = elements.detailMemoryId.dataset.value;
    if (!mid) return;
    try {
      await navigator.clipboard.writeText(mid);
      showToast("Memory ID copied.");
    } catch {
      showToast("Could not copy memory ID.");
    }
  });
  elements.copyHash.addEventListener("click", async () => {
    const hash = elements.copyHash.dataset.hash;
    if (!hash) return;
    try {
      await navigator.clipboard.writeText(hash);
      showToast("Content identity copied.");
    } catch {
      showToast("Could not copy the content identity.");
    }
  });
  elements.deleteMemory.addEventListener("click", showDeleteDialog);
  elements.closeDelete.addEventListener("click", () =>
    elements.deleteDialog.close(),
  );
  elements.cancelDelete.addEventListener("click", () =>
    elements.deleteDialog.close(),
  );
  elements.submitDelete.addEventListener("click", deleteSelectedMemory);

  elements.fileInput.addEventListener("change", () => {
    addSelectedFiles(elements.fileInput.files || []);
  });
  elements.closeImport.addEventListener("click", () =>
    elements.importDialog.close(),
  );
  elements.clearFiles.addEventListener("click", () => {
    if (state.importing) return;
    state.selectedFiles = [];
    elements.fileInput.value = "";
    elements.importMessage.hidden = true;
    renderFileQueue();
  });
  elements.importForm.addEventListener("submit", handleImport);
  elements.importDialog.addEventListener("close", resetImportForm);
  elements.importDialog.addEventListener("cancel", (event) => {
    if (state.importing) event.preventDefault();
  });

  for (const eventName of ["dragenter", "dragover"]) {
    elements.dropZone.addEventListener(eventName, (event) => {
      event.preventDefault();
      elements.dropZone.classList.add("is-dragging");
    });
  }
  for (const eventName of ["dragleave", "drop"]) {
    elements.dropZone.addEventListener(eventName, (event) => {
      event.preventDefault();
      elements.dropZone.classList.remove("is-dragging");
    });
  }
  elements.dropZone.addEventListener("drop", (event) => {
    addSelectedFiles(event.dataTransfer?.files || []);
  });

  document.addEventListener("keydown", (event) => {
    if (
      event.key === "/" &&
      !elements.importDialog.open &&
      !["INPUT", "TEXTAREA"].includes(document.activeElement?.tagName)
    ) {
      event.preventDefault();
      elements.searchQuery.focus();
    }
  });

  document.addEventListener("visibilitychange", scheduleUnderstandingRefresh);
  window.addEventListener("beforeunload", () => {
    stopUnderstandingRefresh();
    revokePreviewURL();
  });
  checkHealth();
  loadMemories();
})();
