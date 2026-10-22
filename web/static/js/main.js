(function () {
  function dismissAlert(alert) {
    if (!alert || alert.dataset.dismissing === 'true') return;
    alert.dataset.dismissing = 'true';
    alert.style.transition = 'opacity 0.35s ease, transform 0.35s ease';
    alert.style.opacity = '0';
    alert.style.transform = 'translateY(-8px)';
    setTimeout(function () {
      if (alert.parentNode) {
        alert.remove();
      }
    }, 350);
  }

  function initApp() {
    // 1. Manual close buttons for alerts
    document.querySelectorAll('.alert-close').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var alert = btn.closest('.alert');
        dismissAlert(alert);
      });
    });

    // 2. Auto-dismiss success/informational alerts after 4 seconds
    setTimeout(function () {
      document.querySelectorAll('.alert-success, .alert-auto-dismiss').forEach(function (alert) {
        dismissAlert(alert);
      });
    }, 4000);

    // 3. Smart Error Dismiss: Error alerts remain visible, BUT if on the login page
    // the user starts typing in username or password, clear the error alert immediately
    var loginErrorAlert = document.getElementById('loginErrorAlert');
    if (loginErrorAlert) {
      var usernameInput = document.getElementById('username');
      var passwordInput = document.getElementById('password');

      var handleTyping = function () {
        dismissAlert(loginErrorAlert);
        if (usernameInput) usernameInput.removeEventListener('input', handleTyping);
        if (passwordInput) passwordInput.removeEventListener('input', handleTyping);
      };

      if (usernameInput) usernameInput.addEventListener('input', handleTyping);
      if (passwordInput) passwordInput.addEventListener('input', handleTyping);
    }

    // 4. Modal open/close helpers
    window.openModal = function (modalId) {
      var modal = document.getElementById(modalId);
      if (modal) {
        modal.classList.add('show');
      }
    };

    window.closeModal = function (modalId) {
      var modal = document.getElementById(modalId);
      if (modal) {
        modal.classList.remove('show');
      }
    };

    // Close modal when clicking on backdrop
    document.querySelectorAll('.modal-backdrop').forEach(function (backdrop) {
      backdrop.addEventListener('click', function (e) {
        if (e.target === backdrop) {
          backdrop.classList.remove('show');
        }
      });
    });

    // 5. Confirm prompts for destructive operations
    document.querySelectorAll('[data-confirm]').forEach(function (el) {
      el.addEventListener('submit', function (e) {
        var msg = el.getAttribute('data-confirm');
        if (!confirm(msg || 'Apakah Anda yakin ingin melakukan tindakan ini?')) {
          e.preventDefault();
        }
      });
    });

    // 6. Reusable password visibility toggle
    document.querySelectorAll('.toggle-password-btn').forEach(function (btn) {
      if (btn.dataset.toggleBound === 'true') return;
      btn.dataset.toggleBound = 'true';

      btn.addEventListener('click', function (e) {
        e.preventDefault();
        var targetId = btn.getAttribute('data-target') || 'password';
        var input = document.getElementById(targetId);
        if (!input) return;

        var isPassword = input.getAttribute('type') === 'password';
        input.setAttribute('type', isPassword ? 'text' : 'password');

        var eyeIcon = btn.querySelector('.eye-icon-show') || document.getElementById('eyeIcon');
        var eyeOffIcon = btn.querySelector('.eye-icon-hide') || document.getElementById('eyeOffIcon');

        if (eyeIcon && eyeOffIcon) {
          if (isPassword) {
            eyeIcon.style.display = 'none';
            eyeOffIcon.style.display = 'block';
            btn.setAttribute('aria-label', 'Sembunyikan password');
            btn.setAttribute('title', 'Sembunyikan password');
          } else {
            eyeIcon.style.display = 'block';
            eyeOffIcon.style.display = 'none';
            btn.setAttribute('aria-label', 'Lihat password');
            btn.setAttribute('title', 'Lihat password');
          }
        }
        input.focus();
      });
    });
  }

  if (document.readyState !== 'loading') {
    initApp();
  } else {
    document.addEventListener('DOMContentLoaded', initApp);
  }
})();
