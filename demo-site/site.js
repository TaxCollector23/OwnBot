const searchPanel = document.querySelector('.docs-search');
const docArticle = document.querySelector('.doc-article');

if (searchPanel && docArticle) {
  const searchInput = searchPanel.querySelector('#docs-search-input');
  const clearButton = searchPanel.querySelector('#docs-search-clear');
  const resultStatus = searchPanel.querySelector('#docs-search-status');
  const sections = Array.from(docArticle.children).filter((child) => child.matches('section[id]'));
  const normalize = (value) => value.normalize('NFKC').replace(/\s+/g, ' ').trim().toLowerCase();
  const indexedSections = sections.map((section) => ({
    element: section,
    text: normalize(section.textContent),
  }));

  if (searchInput && clearButton && resultStatus && indexedSections.length) {
    const updateResults = () => {
      const query = normalize(searchInput.value);
      let matches = 0;

      for (const item of indexedSections) {
        const visible = !query || item.text.includes(query);
        item.element.hidden = !visible;
        if (visible) matches += 1;
      }

      resultStatus.textContent = query
        ? matches
          ? `Showing ${matches} of ${indexedSections.length} sections.`
          : `No matching sections (0 of ${indexedSections.length}). Clear search to show all sections.`
        : `Showing all ${indexedSections.length} sections.`;
    };

    searchInput.addEventListener('input', updateResults);
    searchInput.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && searchInput.value) {
        event.preventDefault();
        searchInput.value = '';
        updateResults();
      }
    });
    clearButton.addEventListener('click', () => {
      searchInput.value = '';
      updateResults();
      searchInput.focus();
    });
    const revealHashTarget = (hash, scroll = false) => {
      let id;
      try { id = decodeURIComponent(hash.slice(1)); } catch { return; }
      const target = document.getElementById(id);
      const section = target?.closest('section[id]');
      if (section && docArticle.contains(section) && section.hidden) {
        searchInput.value = '';
        updateResults();
        if (scroll) window.requestAnimationFrame(() => target.scrollIntoView({block:'start'}));
      }
    };
    document.addEventListener('click', (event) => {
      const link = event.target instanceof Element ? event.target.closest('a[href^="#"]') : null;
      if (link) revealHashTarget(link.hash);
    });
    window.addEventListener('hashchange', () => revealHashTarget(window.location.hash, true));

    searchPanel.hidden = false;
    updateResults();
  }
}

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
