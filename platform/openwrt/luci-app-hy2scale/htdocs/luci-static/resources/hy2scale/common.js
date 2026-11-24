'use strict';
'require baseclass';

// The logo is put inline, coloured for the theme's background: its own
// prefers-color-scheme style follows the browser, not the LuCI theme.
var COLORS = {
	light: { 'hy2s-ring': '#4D6EA3', 'hy2s-core': '#102545', 'hy2s-h': '#102545' },
	dark:  { 'hy2s-ring': '#6f8fc7', 'hy2s-core': '#96b0e2', 'hy2s-h': '#e3e6ed' }
};

var CSS = [
	'.hy2s-title { display: flex; align-items: center }',
	'.hy2s-title svg { width: 1.3em; height: 1.3em; flex: none; margin-right: .4em }',
	'.hy2s-on { color: #3c9a46; font-weight: bold }',
	'.hy2s-off { color: #d9534f; font-weight: bold }',
	'.hy2s-actions { margin: .8em 0 }',
	'.hy2s-actions .cbi-button { margin: 0 .4em .4em 0 }',
	'.hy2s-log { width: 100%; font-family: monospace; font-size: 12px; box-sizing: border-box }'
].join('\n');

var logoText = null;

function backgroundIsDark(el) {
	for (var n = el; n && n.nodeType === 1; n = n.parentNode) {
		var m = String(window.getComputedStyle(n).backgroundColor || '').match(/[\d.]+/g);

		if (m && m.length >= 3 && (m.length < 4 || +m[3] > 0.5))
			return (0.299 * m[0] + 0.587 * m[1] + 0.114 * m[2]) < 128;
	}

	return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
}

function paint(svg) {
	var colors = COLORS[backgroundIsDark(svg.parentNode) ? 'dark' : 'light'];

	Object.keys(colors).forEach(function(cls) {
		svg.querySelectorAll('.' + cls).forEach(function(el) {
			el.setAttribute('fill', colors[cls]);
			if (cls == 'hy2s-ring')
				el.setAttribute('fill-opacity', '.5');
		});
	});

	svg.querySelectorAll('.hy2s-arrow').forEach(function(el) {
		el.setAttribute('fill', 'none');
		el.setAttribute('stroke', '#FEBD02');
	});
}

function paintWhenShown(svg, tries) {
	if (document.body.contains(svg))
		return paint(svg);

	if (tries > 0)
		window.setTimeout(function() { paintWhenShown(svg, tries - 1) }, 50);
}

return baseclass.extend({
	// load: fetch the logo once (a missing logo only leaves it out)
	load: function() {
		if (logoText != null || typeof window.fetch != 'function')
			return Promise.resolve(logoText);

		return window.fetch(L.resource('hy2scale/logo.svg')).then(function(res) {
			return res.ok ? res.text() : '';
		}).then(function(text) {
			logoText = text;
			return text;
		}).catch(function() {
			logoText = '';
			return '';
		});
	},

	logo: function() {
		if (!logoText || typeof window.DOMParser != 'function')
			return null;

		var doc = new DOMParser().parseFromString(logoText, 'image/svg+xml'),
		    svg = doc.documentElement;

		if (!svg || svg.nodeName.toLowerCase() != 'svg')
			return null;

		svg = document.importNode(svg, true);
		svg.querySelectorAll('style').forEach(function(st) { st.parentNode.removeChild(st) });
		svg.removeAttribute('width');
		svg.removeAttribute('height');
		svg.setAttribute('aria-hidden', 'true');
		svg.setAttribute('focusable', 'false');

		paintWhenShown(svg, 100);

		if (window.matchMedia) {
			var mq = window.matchMedia('(prefers-color-scheme: dark)'),
			    repaint = function() { window.setTimeout(function() { paint(svg) }, 100) };

			if (mq.addEventListener)
				mq.addEventListener('change', repaint);
			else if (mq.addListener)
				mq.addListener(repaint);
		}

		return svg;
	},

	// header: the page title with the logo, and this app's few styles
	header: function(title) {
		var logo = this.logo();

		return E('div', {}, [
			E('style', { 'type': 'text/css' }, [ CSS ]),
			E('h2', { 'class': 'hy2s-title', 'name': 'content' }, logo ? [ logo, title ] : [ title ])
		]);
	}
});
