'use strict';
/* theme.js 首屏主题预热（蓝本 workbuddy2api-panel 同款）：由 <head> 以同步脚本加载，且必须在 <style> 之前。
 *
 * <html> 上的 data-theme="dark" 只是无脚本兜底，真实偏好在 localStorage['loomy2api.theme']，
 * 读它的是 app.js——挂在 <body> 末尾，刷新时第一帧早就按深色画完了，会「先闪一下深色」。
 * 提前到这里，第一帧就是最终主题。
 *
 * 不能写成内联脚本：CSP 是 script-src 'self'，内联块会被拦掉。
 * 判定规则必须与 app.js 的 effTheme() 逐字一致，否则预热一套、app.js 再翻一套，二次闪烁。
 */
(function () {
  var saved = 'auto';
  try {
    saved = localStorage.getItem('loomy2api.theme') || 'auto';
  } catch (e) {
    /* 隐私模式：退回跟随系统 */
  }
  var eff = saved === 'auto'
    ? (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark')
    : saved;
  document.documentElement.setAttribute('data-theme', eff);
})();
