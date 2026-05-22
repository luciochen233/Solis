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
});

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
    toast.innerHTML = `
        <div class="toast-message">${message}</div>
        <button class="toast-close" onclick="dismissToast(this)">&times;</button>
    `;
    container.appendChild(toast);
}

