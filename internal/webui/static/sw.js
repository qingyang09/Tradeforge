// TradeForge 的 service worker——目前只负责接收 web push 推送并展示系统通知。
// 不做离线缓存/资源预取：这个应用需要实时数据，离线缓存价值不大，加了反而容易
// 缓存出过期内容，先只实现推送需要的最小功能。
self.addEventListener('push', event => {
  let data = { title: 'TradeForge', body: '' };
  try {
    data = event.data.json();
  } catch (e) {
    // 忽略解析失败，用上面的默认值兜底，不让整个推送处理因为格式问题崩掉。
  }
  event.waitUntil(self.registration.showNotification(data.title, { body: data.body }));
});
