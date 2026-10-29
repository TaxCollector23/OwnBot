for (const block of document.querySelectorAll('.install-command, .code-block')) {
  block.classList.add('has-copy');

  const control = document.createElement('div');
  control.className = 'copy-control';

  const button = document.createElement('button');
  button.className = 'copy-button';
  button.type = 'button';
  button.textContent = 'Copy';
  button.setAttribute('aria-label', 'Copy command or address');

  const status = document.createElement('span');
  status.className = 'sr-only';
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');

  control.append(button, status);
  block.after(control);

  button.addEventListener('click', async () => {
    const value = block.textContent.trim();

    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(value);
      } else {
        const selection = window.getSelection();
        const range = document.createRange();
        range.selectNodeContents(block);
        selection.removeAllRanges();
        selection.addRange(range);

        if (!document.execCommand('copy')) throw new Error('Clipboard unavailable');
        selection.removeAllRanges();
      }

      button.textContent = 'Copied';
      status.textContent = 'Text copied to the clipboard.';
    } catch {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(block);
      selection.removeAllRanges();
      selection.addRange(range);

      button.textContent = 'Selected';
      status.textContent = 'Text selected. Press Command+C on Mac or Ctrl+C on Windows or Linux to copy.';
    }

    window.setTimeout(() => { button.textContent = 'Copy'; }, 1800);
  });
}
