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
