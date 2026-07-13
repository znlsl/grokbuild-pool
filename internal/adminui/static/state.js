/* Shared mutable UI state (admin_key 仅内存，不写 storage) */
export const state = {
  adminKey: null,
  currentPage: "",
  dashBuilt: false,
  pollTimer: null,
  toastTimer: null,
  // 账号列表游标分页
  accPageSize: 50,
  accCursor: "",
  accNextCursor: "",
  accCursorStack: [],
  accPageIndex: 1,
  accLoading: false,
  // route leave animation generation token
  routeGen: 0,
  // next wrapPage should animate enter
  pageAnimate: false
};
