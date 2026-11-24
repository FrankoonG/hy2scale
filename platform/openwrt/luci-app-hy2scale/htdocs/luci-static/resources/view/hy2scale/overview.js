'use strict';
'require view';
'require form';
'require rpc';
'require uci';
'require poll';
'require ui';
'require dom';
'require hy2scale.common as common';

var callStatus = rpc.declare({
	object: 'luci.hy2scale',
	method: 'status',
	expect: { '': {} }
});

var callListeners = rpc.declare({
	object: 'luci.hy2scale',
	method: 'listeners',
	expect: { listeners: [] }
});

var callService = rpc.declare({
	object: 'luci.hy2scale',
	method: 'service',
	params: [ 'action' ],
	expect: { '': {} }
});

var callResetLogin = rpc.declare({
	object: 'luci.hy2scale',
	method: 'reset_login',
	expect: { '': {} }
});

var WAN_PORT_RE = /^(\d{1,5})(?:-(\d{1,5}))?(?:\/(tcp|udp|any|both))?$/;

// parseWanPort: "443", "8000-8010/tcp" -> { from, to, proto }, else null
function parseWanPort(v) {
	var m = WAN_PORT_RE.exec(String(v || ''));

	if (!m)
		return null;

	var a = +m[1], b = (m[2] != null && m[2] !== '') ? +m[2] : a;

	if (a < 1 || b > 65535 || a > b)
		return null;

	return { from: a, to: b, proto: (!m[3] || m[3] == 'both') ? 'any' : m[3] };
}

function isLoopback(addr) {
	return /^127\./.test(addr) || addr == '::1' || /^::ffff:127\./.test(addr);
}

function yesNo(ok) {
	return E('span', { 'class': ok ? 'hy2s-on' : 'hy2s-off' }, [ ok ? _('Available') : _('Missing') ]);
}

return view.extend({
	load: function() {
		return Promise.all([
			callStatus().catch(function() { return {} }),
			callListeners().catch(function() { return [] }),
			uci.load('hy2scale'),
			common.load()
		]);
	},

	// the web UI address: this host, the node's port and base path
	webUrl: function() {
		var st = this.status || {},
		    port = st.ui_port || uci.get('hy2scale', 'main', 'ui_port') || 5565,
		    base = (st.base_path != null) ? st.base_path : (uci.get('hy2scale', 'main', 'base_path') || '/scale'),
		    host = window.location.hostname;

		if (host.indexOf(':') >= 0 && host.charAt(0) != '[')
			host = '[' + host + ']';

		return (st.https === false ? 'http' : 'https') + '://' + host + ':' + port +
			String(base).replace(/\/+$/, '') + '/';
	},

	wanPorts: function() {
		var el = this.wanPortOption ? this.wanPortOption.getUIElement('main') : null,
		    v = el ? el.getValue() : uci.get('hy2scale', 'main', 'wan_port');

		return Array.isArray(v) ? v : (v ? [ v ] : []);
	},

	// allowed: the port is let in from WAN by the settings
	allowed: function(proto, port) {
		var st = this.status || {},
		    uiPort = +(st.ui_port || uci.get('hy2scale', 'main', 'ui_port') || 5565);

		if (proto == 'tcp' && port == uiPort && uci.get('hy2scale', 'main', 'wan_ui') == '1')
			return true;

		return this.wanPorts().some(function(e) {
			var p = parseWanPort(e);
			return p && port >= p.from && port <= p.to && (p.proto == 'any' || p.proto == proto);
		});
	},

	renderStatus: function() {
		var st = this.status || {},
		    state, rows;

		if (st.running)
			state = E('span', { 'class': 'hy2s-on' }, [ _('Running') ]);
		else
			state = E('span', { 'class': 'hy2s-off' }, [ st.enabled ? _('Stopped') : _('Disabled') ]);

		rows = [
			[ _('Service'), state ],
			[ _('Version'), st.version || '-' ],
			[ _('Node'), st.node || '-' ],
			[ _('Memory'), (st.rss_kb != null) ? '%1024.2mB'.format(st.rss_kb * 1024) : '-' ],
			[ _('Uptime'), (st.uptime != null) ? '%t'.format(st.uptime) : '-' ],
			[ _('Free space'), (st.data_free_kb != null) ? '%1024.2mB'.format(st.data_free_kb * 1024) : '-' ],
			[ 'iptables', yesNo(st.iptables) ],
			[ 'TUN', yesNo(st.tun) ]
		];

		dom.content(this.statusTable, rows.map(function(r) {
			return E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td left', 'width': '33%' }, [ r[0] ]),
				E('td', { 'class': 'td left' }, [ r[1] ])
			]);
		}));

		var b = this.buttons;
		b.start.disabled = !!st.running || !st.enabled;
		b.stop.disabled = !st.running;
		b.restart.disabled = !st.running;
		b.open.disabled = !st.running;
		b.reset.disabled = !st.running;
		b.start.title = st.enabled ? '' : _('Not enabled in settings');
	},

	renderListeners: function() {
		var self = this,
		    rows = [
			E('tr', { 'class': 'tr table-titles' }, [
				E('th', { 'class': 'th' }, [ _('Protocol') ]),
				E('th', { 'class': 'th' }, [ _('Port') ]),
				E('th', { 'class': 'th' }, [ _('Address') ]),
				E('th', { 'class': 'th' }, [ _('WAN') ])
			])
		    ];

		(this.listeners || []).forEach(function(l) {
			var action;

			if (isLoopback(l.addr))
				action = E('em', {}, [ _('Local only') ]);
			else if (self.allowed(l.proto, l.port))
				action = E('span', { 'class': 'hy2s-on' }, [ _('Allowed') ]);
			else
				action = E('button', {
					'class': 'cbi-button cbi-button-add',
					'click': ui.createHandlerFn(self, 'handleAllow', l.proto, l.port)
				}, [ _('Allow on WAN') ]);

			rows.push(E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td', 'data-title': _('Protocol') }, [ l.proto.toUpperCase() ]),
				E('td', { 'class': 'td', 'data-title': _('Port') }, [ String(l.port) ]),
				E('td', { 'class': 'td', 'data-title': _('Address') }, [ l.addr || '' ]),
				E('td', { 'class': 'td', 'data-title': _('WAN') }, [ action ])
			]));
		});

		if (rows.length == 1)
			rows.push(E('tr', { 'class': 'tr placeholder' }, [
				E('td', { 'class': 'td', 'colspan': 4 }, [
					E('em', {}, [ (this.status || {}).running ? _('None') : _('Not running') ])
				])
			]));

		dom.content(this.listenerTable, rows);
	},

	refresh: function() {
		var self = this;

		return Promise.all([ callStatus(), callListeners() ]).then(function(r) {
			self.status = r[0] || {};
			self.listeners = r[1] || [];
			self.renderStatus();
			self.renderListeners();
		}).catch(function() {});
	},

	handleService: function(action, ev) {
		var self = this;

		Object.keys(this.buttons).forEach(function(k) { self.buttons[k].disabled = true });

		return callService(action).then(function(res) {
			if (!res || !res.ok)
				ui.addNotification(null, E('p', [ _('Failed: %s').format((res && res.result) || action) ]), 'danger');
		}).catch(function(err) {
			ui.addNotification(null, E('p', [ _('Failed: %s').format(err.message || err) ]), 'danger');
		}).then(function() {
			// the node takes a moment to come up or go away
			return new Promise(function(resolve) { window.setTimeout(resolve, 1500) });
		}).then(function() {
			return self.refresh();
		});
	},

	handleOpen: function(ev) {
		window.open(this.webUrl(), '_blank', 'noopener');
	},

	handleResetLogin: function(ev) {
		var self = this;

		ui.showModal(_('Reset sign-in'), [
			E('p', [ _('Reset the web UI sign-in to admin/admin?') ]),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, [ _('Cancel') ]),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-negative',
					'click': ui.createHandlerFn(self, function() {
						return callResetLogin().then(function(res) {
							ui.hideModal();
							if (res && res.ok)
								ui.addNotification(null, E('p', [ _('Sign-in reset to admin/admin.') ]), 'info');
							else
								ui.addNotification(null, E('p', [ _('Failed: %s').format((res && res.result) || '-') ]), 'danger');
						}).catch(function(err) {
							ui.hideModal();
							ui.addNotification(null, E('p', [ _('Failed: %s').format(err.message || err) ]), 'danger');
						});
					})
				}, [ _('Reset') ])
			])
		]);
	},

	// handleAllow: add PORT/PROTO to the WAN ports, then save and apply
	handleAllow: function(proto, port, ev) {
		var el = this.wanPortOption.getUIElement('main');

		if (!el)
			return;

		var list = this.wanPorts().slice(),
		    entry = port + '/' + proto;

		if (list.indexOf(entry) < 0)
			list.push(entry);

		el.setValue(list);

		return this.handleSaveApply(ev, '0');
	},

	render: function(data) {
		var self = this, m, s, o;

		this.status = data[0] || {};
		this.listeners = data[1] || [];

		m = new form.Map('hy2scale');

		s = m.section(form.NamedSection, 'main', 'hy2scale', _('Settings'));
		s.addremove = false;

		o = s.option(form.Flag, 'enabled', _('Enable'));
		o.rmempty = false;

		o = s.option(form.Value, 'data_dir', _('Data directory'));
		o.placeholder = '/etc/hy2scale';
		o.datatype = 'string';
		o.validate = function(section_id, value) {
			if (value && !/^\/[^\s]*$/.test(value))
				return _('Expecting an absolute path');
			return true;
		};

		o = s.option(form.Value, 'ui_port', _('Web UI port'));
		o.placeholder = '5565';
		o.datatype = 'port';

		o = s.option(form.Value, 'base_path', _('Base path'));
		o.placeholder = '/scale';
		o.datatype = 'string';
		o.validate = function(section_id, value) {
			if (value && !/^\/[A-Za-z0-9._~\/-]*$/.test(value))
				return _('Expecting a path starting with /');
			return true;
		};

		o = s.option(form.Value, 'mem_limit', _('Memory limit (MiB)'));
		o.placeholder = _('No limit');
		o.datatype = 'and(uinteger,min(16))';

		o = s.option(form.Flag, 'wan_ui', _('Web UI on WAN'));
		o.rmempty = false;

		o = s.option(form.DynamicList, 'wan_port', _('WAN ports'),
			_('Port or range, with /tcp, /udp or /any.'));
		o.placeholder = '443/udp';
		o.datatype = 'string';
		o.validate = function(section_id, value) {
			if (value && !/\/both$/.test(value) && parseWanPort(value))
				return true;
			return value ? _('Expecting port[-port][/tcp|udp|any]') : true;
		};
		this.wanPortOption = o;

		this.statusTable = E('table', { 'class': 'table' });
		this.listenerTable = E('table', { 'class': 'table' });

		this.buttons = {
			start: E('button', {
				'class': 'cbi-button cbi-button-apply',
				'click': ui.createHandlerFn(this, 'handleService', 'start')
			}, [ _('Start') ]),
			stop: E('button', {
				'class': 'cbi-button cbi-button-reset',
				'click': ui.createHandlerFn(this, 'handleService', 'stop')
			}, [ _('Stop') ]),
			restart: E('button', {
				'class': 'cbi-button cbi-button-reload',
				'click': ui.createHandlerFn(this, 'handleService', 'restart')
			}, [ _('Restart') ]),
			open: E('button', {
				'class': 'cbi-button cbi-button-action',
				'click': ui.createHandlerFn(this, 'handleOpen')
			}, [ _('Open web UI') ]),
			reset: E('button', {
				'class': 'cbi-button cbi-button-negative',
				'click': ui.createHandlerFn(this, 'handleResetLogin')
			}, [ _('Reset sign-in') ])
		};

		return m.render().then(function(mapEl) {
			var b = self.buttons;

			self.renderStatus();
			self.renderListeners();

			poll.add(L.bind(self.refresh, self), 5);

			return E('div', {}, [
				common.header('hy2scale'),
				E('div', { 'class': 'cbi-section' }, [
					E('h3', [ _('Status') ]),
					self.statusTable,
					E('div', { 'class': 'hy2s-actions' }, [
						b.start, b.stop, b.restart, b.open, b.reset
					])
				]),
				mapEl,
				E('div', { 'class': 'cbi-section' }, [
					E('h3', [ _('Listening ports') ]),
					self.listenerTable
				])
			]);
		});
	}
});
