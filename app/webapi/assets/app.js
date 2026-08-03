// global helpers for the Shield web ui.

// copyUserID copies a telegram user id to the clipboard and gives the button feedback.
function copyUserID(userId, btn) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(String(userId)).then(function () {
            if (btn) {
                btn.textContent = 'Copied!';
                btn.classList.add('btn-ham');
                setTimeout(function () {
                    btn.textContent = 'Copy ID';
                    btn.classList.remove('btn-ham');
                }, 2000);
            }
        }).catch(function () {
            if (btn) {
                btn.textContent = 'Failed to copy';
                setTimeout(function () { btn.textContent = 'Copy ID'; }, 2000);
            }
        });
    } else if (btn) {
        btn.textContent = 'Failed to copy';
        setTimeout(function () { btn.textContent = 'Copy ID'; }, 2000);
    }
}

// re-initialize Alpine components inside fragments swapped in by HTMX.
document.body.addEventListener('htmx:afterSettle', function (evt) {
    if (window.Alpine && evt.detail && evt.detail.target) {
        window.Alpine.initTree(evt.detail.target);
    }
});

// Open native date controls from a clear, full-size button. Some desktop
// browsers render datetime-local as text segments, so the contests form uses a
// dedicated date input and this explicit mouse-friendly calendar trigger.
document.addEventListener('click', function (evt) {
    var trigger = evt.target.closest('[data-date-picker-trigger]');
    if (!trigger) return;

    var input = document.getElementById(trigger.getAttribute('data-date-picker-trigger'));
    if (!input) return;

    input.focus();
    if (typeof input.showPicker === 'function') {
        try {
            input.showPicker();
        } catch (err) {
            // focus() still exposes the native control on browsers that reject
            // showPicker despite reporting it.
        }
    } else {
        input.click();
    }
});
