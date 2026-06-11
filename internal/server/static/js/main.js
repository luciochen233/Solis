// Solis Client-side Interactions

document.addEventListener("DOMContentLoaded", () => {
    // 1. Automatically configure HTMX requests to include CSRF token
    document.addEventListener("htmx:configRequest", (evt) => {
        const csrfToken = document.querySelector('meta[name="csrf-token"]')?.getAttribute('content');
        if (csrfToken) {
            evt.detail.headers['X-CSRF-Token'] = csrfToken;
        }
    });

    // 2. Setup dynamic toast auto-dismissals
    setupToastListeners();

    // 3. Room-tag locking on the item create/edit form
    initRoomTagLock();

    // 4. Initialize drag and drop mechanics for directories and assets
    initDragAndDrop();

    // 5. Mobile sidebar drawer interactions
    initMobileSidebar();

    // 6. Container array drawer grid (3D view)
    initDrawerArray();
});

// Mobile off-canvas sidebar drawer
function toggleSidebar() {
    const sidebar = document.getElementById("sidebar");
    if (!sidebar) return;
    sidebar.classList.contains("open") ? closeSidebar() : openSidebar();
}

function openSidebar() {
    const sidebar = document.getElementById("sidebar");
    const backdrop = document.querySelector(".sidebar-backdrop");
    if (sidebar) sidebar.classList.add("open");
    if (backdrop) backdrop.classList.add("show");
}

function closeSidebar() {
    const sidebar = document.getElementById("sidebar");
    const backdrop = document.querySelector(".sidebar-backdrop");
    if (sidebar) sidebar.classList.remove("open");
    if (backdrop) backdrop.classList.remove("show");
}

function initMobileSidebar() {
    // Close the drawer after tapping a nav link so the destination is visible
    document.querySelectorAll(".sidebar .nav-link").forEach(link => {
        link.addEventListener("click", closeSidebar);
    });

    // Close on Escape for keyboard/accessibility
    document.addEventListener("keydown", (e) => {
        if (e.key === "Escape") closeSidebar();
    });
}

function setupToastListeners() {
    const observer = new MutationObserver((mutations) => {
        mutations.forEach((mutation) => {
            mutation.addedNodes.forEach((node) => {
                if (node.nodeType === Node.ELEMENT_NODE && node.classList.contains('toast')) {
                    autoDismissToast(node);
                }
            });
        });
    });

    const toastContainer = document.getElementById('toast-container');
    if (toastContainer) {
        observer.observe(toastContainer, { childList: true });
        // Dismiss any initial toasts
        toastContainer.querySelectorAll('.toast').forEach(autoDismissToast);
    }
}

function autoDismissToast(toast) {
    // Fade out and remove after 4 seconds
    setTimeout(() => {
        toast.style.opacity = '0';
        toast.style.transform = 'translateY(1rem)';
        toast.style.transition = 'opacity 0.5s ease, transform 0.5s ease';
        setTimeout(() => {
            toast.remove();
        }, 500);
    }, 4000);
}

function dismissToast(button) {
    const toast = button.closest('.toast');
    if (toast) {
        toast.style.opacity = '0';
        toast.style.transform = 'translateY(1rem)';
        toast.style.transition = 'opacity 0.3s ease, transform 0.3s ease';
        setTimeout(() => {
            toast.remove();
        }, 300);
    }
}

function initRoomTagLock() {
    const locationSelect = document.getElementById("location_id");
    if (!locationSelect) return;

    // Track the checkbox that is currently locked as the room tag
    let lockedCheckbox = null;

    function updateRoomTag() {
        const selectedOption = locationSelect.options[locationSelect.selectedIndex];
        if (!selectedOption) return;
        const activeRoomName = selectedOption.text.trim();

        // Unlock the previously locked checkbox before locking the new one
        if (lockedCheckbox) {
            lockedCheckbox.disabled = false;
            // Only uncheck it if it was previously auto-checked (i.e., it IS a room tag).
            // We leave non-room tags that the user manually checked alone.
            lockedCheckbox.checked = false;
            lockedCheckbox = null;
        }

        // Find and lock the checkbox matching the new active room name
        const checkboxes = document.querySelectorAll('input[name="tag_ids"]');
        checkboxes.forEach(cb => {
            const tagName = cb.getAttribute('data-tag-name');
            if (tagName === activeRoomName) {
                cb.checked = true;
                cb.disabled = true;
                lockedCheckbox = cb;
            }
        });
    }

    locationSelect.addEventListener("change", updateRoomTag);
    // Run immediately on page load to lock the default/current room tag
    updateRoomTag();
}

// 4. Dropdown Action Menu Manager
function toggleActionsDropdown(btn, event) {
    if (event) {
        event.stopPropagation();
        event.preventDefault();
    }
    const container = btn.parentElement;
    const isShown = container.classList.contains("show");
    
    // Close any other open dropdowns first
    document.querySelectorAll(".actions-dropdown").forEach(d => {
        d.classList.remove("show");
    });
    
    if (!isShown) {
        container.classList.add("show");
    }
}

// Close dropdowns on outside click
document.addEventListener("click", () => {
    document.querySelectorAll(".actions-dropdown").forEach(d => {
        d.classList.remove("show");
    });
});

// 5. Drag & Drop Directory Move Management
function initDragAndDrop() {
    const draggables = document.querySelectorAll("[draggable='true']");
    const dropzones = document.querySelectorAll(".dropzone");
    
    let draggedElement = null;

    draggables.forEach(draggable => {
        draggable.addEventListener("dragstart", (e) => {
            draggedElement = draggable;
            draggable.classList.add("dragging");
            
            const type = draggable.dataset.type;
            const id = type === "location" ? draggable.dataset.locationId : draggable.dataset.itemId;
            
            e.dataTransfer.setData("text/plain", JSON.stringify({ id, type }));
            e.dataTransfer.effectAllowed = "move";
        });

        draggable.addEventListener("dragend", () => {
            draggable.classList.remove("dragging");
            draggedElement = null;
            dropzones.forEach(zone => zone.classList.remove("drop-active"));
        });
    });

    dropzones.forEach(zone => {
        zone.addEventListener("dragover", (e) => {
            e.preventDefault(); // Required to allow drop!
            
            if (!draggedElement) return;
            
            const draggedType = draggedElement.dataset.type;
            const draggedId = draggedType === "location" ? draggedElement.dataset.locationId : draggedElement.dataset.itemId;
            const zoneLocId = zone.dataset.locationId;
            
            // Prevent folder dropping on itself
            if (draggedType === "location" && draggedId === zoneLocId) {
                return;
            }
            
            zone.classList.add("drop-active");
        });

        zone.addEventListener("dragleave", () => {
            zone.classList.remove("drop-active");
        });

        zone.addEventListener("drop", async (e) => {
            e.preventDefault();
            zone.classList.remove("drop-active");

            try {
                const rawData = e.dataTransfer.getData("text/plain");
                if (!rawData) return;
                const data = JSON.parse(rawData);
                const targetLocationId = zone.dataset.locationId;

                if (!data.id || !targetLocationId) return;

                const csrfToken = document.querySelector('meta[name="csrf-token"]')?.getAttribute('content');

                if (data.type === "item") {
                    // Move item to a location
                    const response = await fetch("/admin/items/move", {
                        method: "POST",
                        headers: {
                            "Content-Type": "application/x-www-form-urlencoded",
                            "X-CSRF-Token": csrfToken
                        },
                        body: new URLSearchParams({
                            "item_id": data.id,
                            "location_id": targetLocationId,
                            "csrf_token": csrfToken
                        })
                    });

                    if (response.ok) {
                        showSuccessToast("Asset moved successfully!");
                        setTimeout(() => window.location.reload(), 1000);
                    } else {
                        const errMsg = await response.text();
                        showErrorToast(errMsg || "Failed to move asset");
                    }
                } else if (data.type === "location") {
                    // Move location inside another location
                    const response = await fetch("/admin/locations/move", {
                        method: "POST",
                        headers: {
                            "Content-Type": "application/x-www-form-urlencoded",
                            "X-CSRF-Token": csrfToken
                        },
                        body: new URLSearchParams({
                            "location_id": data.id,
                            "parent_id": targetLocationId,
                            "csrf_token": csrfToken
                        })
                    });

                    if (response.ok) {
                        showSuccessToast("Folder moved successfully!");
                        setTimeout(() => window.location.reload(), 1000);
                    } else {
                        const errMsg = await response.text();
                        showErrorToast(errMsg || "Failed to move folder");
                    }
                }
            } catch (err) {
                console.error("Error during drag-and-drop drop event:", err);
                showErrorToast("An error occurred during drag-and-drop");
            }
        });
    });
}

function showSuccessToast(message) {
    showToast(message, "success");
}

function showErrorToast(message) {
    showToast(message, "error");
}

function showToast(message, type) {
    const container = document.getElementById("toast-container");
    if (!container) return;

    const toast = document.createElement("div");
    toast.className = `toast ${type === "success" ? "toast-success" : "toast-error"}`;

    const msgDiv = document.createElement("div");
    msgDiv.className = "toast-message";
    msgDiv.textContent = message;

    const closeBtn = document.createElement("button");
    closeBtn.className = "toast-close";
    closeBtn.textContent = "×";
    closeBtn.addEventListener("click", function() { dismissToast(closeBtn); });

    toast.appendChild(msgDiv);
    toast.appendChild(closeBtn);
    container.appendChild(toast);
}

// ==========================================
// 6. Container Array Drawer Grid (3D view)
// ==========================================

function initDrawerArray() {
    const cabinet = document.getElementById("drawer-cabinet");
    if (!cabinet) return;

    const cells = Array.from(cabinet.querySelectorAll(".drawer-cell.has-drawer"));

    // --- Content summary peek -------------------------------------------
    // Peeks are relocated to <body> so the scrollable cabinet wrapper and
    // 3D transforms can't clip them; they are fixed-positioned per cell.
    let activePeek = null;
    let peekHideTimer = null;

    function peekFor(cell) {
        let peek = cell._peek;
        if (!peek) {
            peek = cell.querySelector(".drawer-peek");
            if (!peek) return null;
            document.body.appendChild(peek);
            cell._peek = peek;
            // Keep the peek open while the cursor is over it
            peek.addEventListener("mouseenter", () => clearTimeout(peekHideTimer));
            peek.addEventListener("mouseleave", scheduleHidePeek);
        }
        return peek;
    }

    function showPeek(cell) {
        clearTimeout(peekHideTimer);
        if (activePeek && activePeek.cell !== cell) hidePeek();

        const peek = peekFor(cell);
        if (!peek) return;

        peek.classList.add("show");
        const rect = cell.getBoundingClientRect();
        const peekRect = peek.getBoundingClientRect();
        let left = rect.left + rect.width / 2 - peekRect.width / 2;
        left = Math.max(8, Math.min(left, window.innerWidth - peekRect.width - 8));
        let top = rect.top - peekRect.height - 10;
        if (top < 8) top = rect.bottom + 10;
        peek.style.left = left + "px";
        peek.style.top = top + "px";

        activePeek = { cell, peek };
        cell.classList.add("open");
    }

    function hidePeek() {
        if (!activePeek) return;
        activePeek.peek.classList.remove("show");
        activePeek.cell.classList.remove("open");
        activePeek = null;
    }

    function scheduleHidePeek() {
        clearTimeout(peekHideTimer);
        peekHideTimer = setTimeout(hidePeek, 180);
    }

    const supportsHover = window.matchMedia("(hover: hover)").matches;

    cells.forEach(cell => {
        if (supportsHover) {
            cell.addEventListener("mouseenter", () => { if (!dragState.active) showPeek(cell); });
            cell.addEventListener("mouseleave", scheduleHidePeek);
        }
    });

    document.addEventListener("scroll", hidePeek, true);
    window.addEventListener("resize", hidePeek);
    // Tap outside any drawer closes the peek (mobile)
    document.addEventListener("click", (e) => {
        if (!e.target.closest(".drawer-cell") && !e.target.closest(".drawer-peek")) hidePeek();
    });

    // --- Drag to swap (mouse drag / touch long-press) --------------------
    const dragState = { active: false, cell: null, ghost: null, target: null, suppressClick: false };

    function startDrag(cell, x, y) {
        dragState.active = true;
        dragState.cell = cell;
        cell.classList.add("drag-source");
        hidePeek();

        const front = cell.querySelector(".drawer-front");
        const rect = front.getBoundingClientRect();
        const ghost = front.cloneNode(true);
        ghost.classList.add("drawer-drag-ghost");
        ghost.style.position = "fixed";
        ghost.style.inset = "auto";
        ghost.style.width = rect.width + "px";
        ghost.style.height = rect.height + "px";
        document.body.appendChild(ghost);
        dragState.ghost = ghost;
        moveDrag(x, y);

        if (navigator.vibrate) navigator.vibrate(30);
    }

    function moveDrag(x, y) {
        if (!dragState.active) return;
        dragState.ghost.style.left = x + "px";
        dragState.ghost.style.top = y + "px";

        dragState.ghost.style.display = "none";
        const under = document.elementFromPoint(x, y);
        dragState.ghost.style.display = "";

        const target = under ? under.closest(".drawer-cell") : null;
        if (dragState.target && dragState.target !== target) {
            dragState.target.classList.remove("drop-target");
        }
        if (target && target !== dragState.cell && cabinet.contains(target)) {
            target.classList.add("drop-target");
            dragState.target = target;
        } else {
            dragState.target = null;
        }
    }

    async function endDrag() {
        if (!dragState.active) return;
        const cell = dragState.cell;
        const ghost = dragState.ghost;
        const target = dragState.target;
        dragState.active = false;
        dragState.suppressClick = true;
        setTimeout(() => { dragState.suppressClick = false; }, 50);

        cell.classList.remove("drag-source");
        if (ghost) ghost.remove();
        if (target) target.classList.remove("drop-target");
        dragState.cell = dragState.ghost = dragState.target = null;

        if (!target || target === cell) return;

        const csrfToken = document.querySelector('meta[name="csrf-token"]')?.getAttribute('content');
        try {
            const response = await fetch("/admin/drawers/move", {
                method: "POST",
                headers: {
                    "Content-Type": "application/x-www-form-urlencoded",
                    "X-CSRF-Token": csrfToken
                },
                body: new URLSearchParams({
                    "drawer_id": cell.dataset.drawerId,
                    "row": target.dataset.row,
                    "col": target.dataset.col,
                    "csrf_token": csrfToken
                })
            });
            if (response.ok) {
                showSuccessToast("Drawer moved successfully!");
                setTimeout(() => window.location.reload(), 600);
            } else {
                const errMsg = await response.text();
                showErrorToast(errMsg || "Failed to move drawer");
            }
        } catch (err) {
            console.error("Drawer move failed:", err);
            showErrorToast("An error occurred while moving the drawer");
        }
    }

    cells.forEach(cell => {
        const tray = cell.querySelector(".drawer-tray");

        // Mouse: drag starts after a small movement threshold; plain click navigates
        tray.addEventListener("mousedown", (e) => {
            if (e.button !== 0) return;
            const startX = e.clientX, startY = e.clientY;
            let started = false;

            const onMove = (ev) => {
                if (!started && Math.hypot(ev.clientX - startX, ev.clientY - startY) > 6) {
                    started = true;
                    startDrag(cell, ev.clientX, ev.clientY);
                }
                if (started) moveDrag(ev.clientX, ev.clientY);
            };
            const onUp = () => {
                document.removeEventListener("mousemove", onMove);
                document.removeEventListener("mouseup", onUp);
                endDrag();
            };
            document.addEventListener("mousemove", onMove);
            document.addEventListener("mouseup", onUp);
        });

        // Touch: long-press (450ms) starts drag; quick tap toggles the peek
        tray.addEventListener("touchstart", (e) => {
            const touch = e.touches[0];
            const startX = touch.clientX, startY = touch.clientY;
            let pressTimer = setTimeout(() => {
                pressTimer = null;
                startDrag(cell, startX, startY);
            }, 450);

            const onTouchMove = (ev) => {
                const t = ev.touches[0];
                if (dragState.active) {
                    ev.preventDefault(); // keep the page from scrolling while dragging
                    moveDrag(t.clientX, t.clientY);
                } else if (pressTimer && Math.hypot(t.clientX - startX, t.clientY - startY) > 10) {
                    clearTimeout(pressTimer); // finger is scrolling, not holding
                    pressTimer = null;
                }
            };
            const onTouchEnd = () => {
                tray.removeEventListener("touchmove", onTouchMove);
                tray.removeEventListener("touchend", onTouchEnd);
                tray.removeEventListener("touchcancel", onTouchEnd);
                if (pressTimer) clearTimeout(pressTimer);
                endDrag();
            };
            tray.addEventListener("touchmove", onTouchMove, { passive: false });
            tray.addEventListener("touchend", onTouchEnd);
            tray.addEventListener("touchcancel", onTouchEnd);
        }, { passive: true });

        // Click: navigate on hover devices, toggle the peek on touch devices
        tray.addEventListener("click", (e) => {
            if (dragState.suppressClick) { e.preventDefault(); return; }
            if (supportsHover) {
                window.location.href = "/" + cell.dataset.drawerSlug;
            } else {
                cell.classList.contains("open") ? hidePeek() : showPeek(cell);
            }
        });
    });

    // Block the long-press context menu over drawers (mobile drag)
    cabinet.addEventListener("contextmenu", (e) => {
        if (e.target.closest(".drawer-cell.has-drawer")) e.preventDefault();
    });

    // Keyboard access for empty slots
    cabinet.querySelectorAll(".drawer-cell.empty").forEach(cell => {
        cell.addEventListener("keydown", (e) => {
            if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                openDrawerModal(cell);
            }
        });
    });
}

// --- Drawer create/edit modal --------------------------------------------

function openDrawerModal(cell) {
    const modal = document.getElementById("drawer-modal");
    if (!modal) return;

    const form = document.getElementById("drawer-modal-form");
    const title = document.getElementById("drawer-modal-title");
    const nameInput = document.getElementById("drawer-form-name");
    const colorInput = document.getElementById("drawer-form-color");
    const locationGroup = document.getElementById("drawer-form-location-group");
    const parentSelect = document.getElementById("drawer-form-parent");

    document.getElementById("drawer-form-row").value = cell.dataset.row;
    document.getElementById("drawer-form-col").value = cell.dataset.col;

    if (cell.dataset.drawerId) {
        // Edit existing drawer
        form.action = "/admin/drawers/edit/" + cell.dataset.drawerId;
        title.textContent = modal.dataset.titleEdit;
        nameInput.value = cell.dataset.drawerName || "";
        colorInput.value = /^#[0-9a-fA-F]{6}$/.test(cell.dataset.drawerColor) ? cell.dataset.drawerColor : "#d97706";
        locationGroup.hidden = false;
        if (parentSelect) parentSelect.selectedIndex = 0;
    } else {
        // Create a drawer in an empty slot
        form.action = "/admin/drawers/new";
        title.textContent = modal.dataset.titleCreate;
        nameInput.value = "";
        colorInput.value = "#d97706";
        locationGroup.hidden = true;
    }

    modal.hidden = false;
    nameInput.focus();
}

function closeDrawerModal() {
    const modal = document.getElementById("drawer-modal");
    if (modal) modal.hidden = true;
}

document.addEventListener("click", (e) => {
    if (e.target.id === "drawer-modal") closeDrawerModal();
});

document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeDrawerModal();
});

function setDrawerColor(hex) {
    const colorInput = document.getElementById("drawer-form-color");
    if (colorInput) colorInput.value = hex;
}

