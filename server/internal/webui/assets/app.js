(() => {
  "use strict";

  const apiBase = "/api/v0";
  const maxBlobBytes = 100 * 1024 * 1024;
  const maxAutomaticPreviewBytes = 25 * 1024 * 1024;

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
  };

  const elements = {
    vaultStatus: document.querySelector("#vault-status"),
    vaultStatusLabel: document.querySelector("#vault-status-label"),
    memoryCount: document.querySelector("#memory-count"),
    memoryFilter: document.querySelector("#memory-filter"),
    memoryList: document.querySelector("#memory-list"),
    loadMore: document.querySelector("#load-more"),
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
    previewStage: document.querySelector("#preview-stage"),
    metadataList: document.querySelector("#metadata-list"),
    detailMemoryId: document.querySelector("#detail-memory-id"),
    copyMemoryId: document.querySelector("#copy-memory-id"),
    detailHash: document.querySelector("#detail-hash"),
    copyHash: document.querySelector("#copy-hash"),
    backToList: document.querySelector("#back-to-list"),
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

  async function requestJSON(path) {
    const response = await fetch(path, {
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

  function visibleMemories() {
    const query = elements.memoryFilter.value.trim().toLocaleLowerCase();
    if (!query) return state.memories;

    return state.memories.filter((memory) => {
      const searchable = [
        memory.original_filename,
        memory.media_type,
        memory.blob_hash,
      ]
        .join(" ")
        .toLocaleLowerCase();
      return searchable.includes(query);
    });
  }

  function renderMemoryList() {
    elements.memoryList.replaceChildren();
    elements.memoryCount.textContent = state.memories.length;
    const memories = visibleMemories();

    if (memories.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-list";
      empty.textContent = state.memories.length
        ? "No loaded memories match this filter."
        : "Your Vault is quiet for now. Add a memory to begin.";
      elements.memoryList.append(empty);
    }

    for (const memory of memories) {
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

  function renderNoSelectionState() {
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

      if (!append && state.selectedID === null) {
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

  async function selectMemory(memoryID, { openMobile = true } = {}) {
    state.selectedID = memoryID;
    renderMemoryList();
    setDetailLoading(true);
    if (openMobile) document.body.classList.add("is-detail-open");

    try {
      const detail = await requestJSON(`${apiBase}/memories/${memoryID}`);
      if (state.selectedID !== memoryID) return;
      renderDetail(detail);
    } catch (error) {
      if (state.selectedID !== memoryID) return;
      showToast(`Could not open memory. ${error.message}`);
      state.selectedID = null;
      renderMemoryList();
      renderNoSelectionState();
    } finally {
      if (state.selectedID === memoryID) setDetailLoading(false);
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
      if (!response.ok) throw new Error(`Download failed (${response.status})`);
      const blob = await response.blob();
      if (state.selectedID !== memory.id) return;
      elements.previewStage.replaceChildren();

      if (
        mediaType.startsWith("text/") ||
        mediaType.includes("json") ||
        mediaType.includes("xml")
      ) {
        const pre = document.createElement("pre");
        pre.className = "text-preview";
        pre.textContent = await blob.text();
        elements.previewStage.append(pre);
        return;
      }

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
      if (state.selectedID !== memory.id) return;
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
    const memory = state.memories.find(
      (entry) => entry.id === state.selectedID,
    );
    elements.deleteTitle.textContent =
      memory?.original_filename || "This memory";
    elements.deleteDialog.showModal();
  }

  async function deleteSelectedMemory() {
    const memoryID = state.selectedID;
    if (!memoryID) return;
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
      state.memories = state.memories.filter(
        (entry) => entry.id !== memoryID,
      );
      const deletedMemoryWasSelected = state.selectedID === memoryID;
      if (deletedMemoryWasSelected) {
        state.selectedID = null;
        revokePreviewURL();
      }
      renderMemoryList();
      showToast("Memory deleted.");
      if (deletedMemoryWasSelected) {
        renderNoSelectionState();
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
  elements.memoryFilter.addEventListener("input", renderMemoryList);
  elements.loadMore.addEventListener("click", () =>
    loadMemories({ append: true }),
  );
  elements.backToList.addEventListener("click", () => {
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
      elements.memoryFilter.focus();
    }
  });

  window.addEventListener("beforeunload", revokePreviewURL);
  checkHealth();
  loadMemories();
})();
