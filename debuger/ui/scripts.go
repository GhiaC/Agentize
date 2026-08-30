package ui

// GetScripts returns the JavaScript for the debug interface
func GetScripts() string {
	return `
	        document.addEventListener('DOMContentLoaded', function() {
	            var content = document.getElementById('dashboard-content');
	            var navigationController = null;

	            // Content can be replaced without remounting the app shell. Event
	            // delegation keeps expandable rows working after every navigation.
	            if (content) {
	                content.addEventListener('click', function(e) {
	                    var expandable = e.target.closest('.expandable-content');
	                    if (!expandable || !content.contains(expandable)) return;
	                    e.stopPropagation();
	                    expandable.classList.toggle('expanded');
	                });
	            }

            // Mobile sidebar toggle
            var app = document.getElementById('app');
            var toggle = document.getElementById('sidebar-toggle');
            var backdrop = document.getElementById('sidebar-backdrop');
            if (toggle && app) {
                toggle.addEventListener('click', function() { app.classList.toggle('sidebar-open'); });
            }
            if (backdrop && app) {
                backdrop.addEventListener('click', function() { app.classList.remove('sidebar-open'); });
            }

            // Theme toggle (shares the 'agentize-theme' key with the docs UI)
            var themeBtn = document.getElementById('theme-toggle');
	            if (themeBtn) {
                themeBtn.addEventListener('click', function() {
                    var cur = document.documentElement.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark';
                    document.documentElement.setAttribute('data-bs-theme', cur);
                    try { localStorage.setItem('agentize-theme', cur); } catch (e) {}
	                });
	            }

	            function isDashboardURL(url) {
	                return url.origin === window.location.origin &&
	                    (url.pathname === '/agentize/debug' || url.pathname.indexOf('/agentize/debug/') === 0);
	            }

	            function shouldHandleLink(event, link) {
	                if (!content || event.defaultPrevented || event.button !== 0 ||
	                    event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return false;
	                if (!link || link.target || link.hasAttribute('download')) return false;
	                var url;
	                try { url = new URL(link.href, window.location.href); } catch (e) { return false; }
	                if (!isDashboardURL(url)) return false;
	                return !(url.pathname === window.location.pathname &&
	                    url.search === window.location.search && url.hash);
	            }

	            function syncShell(nextDocument) {
	                var nextTitle = nextDocument.querySelector('.topbar-title');
	                var title = document.querySelector('.topbar-title');
	                if (title && nextTitle) title.textContent = nextTitle.textContent;
	                document.title = nextDocument.title || document.title;

	                var nextActive = nextDocument.querySelector('.sidebar .nav-link-item.active');
	                document.querySelectorAll('.sidebar .nav-link-item').forEach(function(link) {
	                    link.classList.toggle('active', !!nextActive &&
	                        new URL(link.href, window.location.href).pathname ===
	                        new URL(nextActive.href, window.location.href).pathname);
	                });
	            }

	            function activateScripts(root) {
	                var scripts = Array.prototype.slice.call(root.querySelectorAll('script'));
	                return scripts.reduce(function(chain, oldScript) {
	                    return chain.then(function() {
	                        return new Promise(function(resolve) {
	                            var script = document.createElement('script');
	                            Array.prototype.forEach.call(oldScript.attributes, function(attr) {
	                                script.setAttribute(attr.name, attr.value);
	                            });
	                            script.textContent = oldScript.textContent;
	                            if (script.src) {
	                                script.addEventListener('load', resolve, { once: true });
	                                script.addEventListener('error', resolve, { once: true });
	                            }
	                            oldScript.replaceWith(script);
	                            if (!script.src) resolve();
	                        });
	                    });
	                }, Promise.resolve());
	            }

	            function navigate(url, pushState) {
	                if (!content) { window.location.assign(url.href); return Promise.resolve(); }
	                if (navigationController) navigationController.abort();
	                var controller = new AbortController();
	                navigationController = controller;
	                content.classList.add('is-loading');
	                content.setAttribute('aria-busy', 'true');

	                return fetch(url.href, {
	                    signal: controller.signal,
	                    credentials: 'same-origin',
	                    headers: { 'X-Agentize-Navigation': 'content' }
	                }).then(function(response) {
	                    if (!response.ok) throw new Error('Navigation failed: ' + response.status);
	                    return response.text();
	                }).then(function(html) {
	                    var nextDocument = new DOMParser().parseFromString(html, 'text/html');
	                    var nextContent = nextDocument.getElementById('dashboard-content');
	                    if (!nextContent) throw new Error('Dashboard content was not found');
	                    if (navigationController !== controller) return;
	                    window.dispatchEvent(new CustomEvent('agentize:before-content-replace'));
	                    content.innerHTML = nextContent.innerHTML;
	                    content.scrollTop = 0;
	                    syncShell(nextDocument);
	                    if (pushState) window.history.pushState({}, '', url.href);
	                    app.classList.remove('sidebar-open');
	                    return activateScripts(content).then(function() {
	                        window.dispatchEvent(new CustomEvent('agentize:content-ready'));
	                    });
	                }).catch(function(error) {
	                    if (error.name === 'AbortError') return;
	                    window.location.assign(url.href);
	                }).finally(function() {
	                    if (navigationController === controller) {
	                        navigationController = null;
	                        content.classList.remove('is-loading');
	                        content.removeAttribute('aria-busy');
	                    }
	                });
	            }

	            document.addEventListener('click', function(event) {
	                var link = event.target.closest('a[href]');
	                if (!shouldHandleLink(event, link)) return;
	                event.preventDefault();
	                navigate(new URL(link.href, window.location.href), true);
	            });

	            window.addEventListener('popstate', function() {
	                var url = new URL(window.location.href);
	                if (isDashboardURL(url)) navigate(url, false);
	                else window.location.reload();
	            });
	        });
	    `
}

// GetBootstrapJS returns the Bootstrap JavaScript CDN URL
func GetBootstrapJS() string {
	return `https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/js/bootstrap.bundle.min.js`
}

// GetBootstrapJSIntegrity returns the integrity hash for Bootstrap JS
func GetBootstrapJSIntegrity() string {
	return `sha384-BBtl+eGJRgqQAUMxJ7pMwbEyER4l1g+O15P+16Ep7Q9Q+zqX6gSbd85u4mG4QzX+`
}
