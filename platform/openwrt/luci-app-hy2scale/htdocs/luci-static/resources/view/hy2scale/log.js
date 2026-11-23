'use strict';
'require view';
'require rpc';
'require ui';
'require hy2scale.common as common';

var callLogs = rpc.declare({
	object: 'luci.hy2scale',
	method: 'logs',
	params: [ 'lines' ],
	expect: { log: '' }
});

return view.extend({
	lines: 200,

	load: function() {
		return Promise.all([
			callLogs(this.lines).catch(function() { return '' }),
			common.load()
		]);
	},

	show: function(text) {
		var ta = this.textarea;

		ta.value = text || '';
		ta.placeholder = _('No log entries');
		window.setTimeout(function() { ta.scrollTop = ta.scrollHeight }, 0);
	},

	handleRefresh: function(ev) {
		var self = this;

		return callLogs(this.lines).then(function(text) {
			self.show(text);
		}).catch(function(err) {
			ui.addNotification(null, E('p', [ _('Failed: %s').format(err.message || err) ]), 'danger');
		});
	},

	render: function(data) {
		var self = this,
		    sizes = [ 100, 200, 500, 1000 ];

		this.textarea = E('textarea', {
			'class': 'cbi-input-textarea hy2s-log',
			'readonly': 'readonly',
			'wrap': 'off',
			'rows': 30
		});

		var select = E('select', {
			'class': 'cbi-input-select',
			'style': 'width: auto',
			'change': function(ev) {
				self.lines = +ev.target.value;
				return self.handleRefresh(ev);
			}
		}, sizes.map(function(n) {
			return E('option', { 'value': n, 'selected': (n == self.lines) ? 'selected' : null }, [ _('%d lines').format(n) ]);
		}));

		this.show(data[0]);

		return E('div', {}, [
			common.header('hy2scale'),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', [ _('Log') ]),
				E('div', { 'class': 'hy2s-actions' }, [
					select,
					' ',
					E('button', {
						'class': 'cbi-button cbi-button-reload',
						'click': ui.createHandlerFn(this, 'handleRefresh')
					}, [ _('Refresh') ])
				]),
				this.textarea
			])
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
