/* Settings form field builders */
import { esc } from "./util.js";

export function field(label, id, val, type, attrs) {
  type = type || "number";
  attrs = attrs || "";
  var v = val == null ? "" : String(val);
  return '<div class="field"><label for="' + id + '">' + label + '</label><input id="' + id +
    '" class="input" type="' + type + '" value="' + esc(v) + '" ' + attrs + " /></div>";
}

export function fieldText(label, id, val, placeholder) {
  return field(label, id, val, "text", placeholder ? 'placeholder="' + esc(placeholder) + '"' : "");
}

export function fieldSelect(label, id, val, options) {
  var opts = (options || []).map(function (o) {
    var sel = String(o.v) === String(val) ? " selected" : "";
    return '<option value="' + esc(String(o.v)) + '"' + sel + ">" + esc(o.l) + "</option>";
  }).join("");
  return '<div class="field"><label for="' + id + '">' + label + '</label><select id="' + id +
    '" class="input">' + opts + "</select></div>";
}

export function fieldBool(label, id, val) {
  return fieldSelect(label, id, val ? "1" : "0", [
    { v: "0", l: "否" },
    { v: "1", l: "是" }
  ]);
}

export function fieldArea(label, id, val, rows) {
  rows = rows || 6;
  var v = val == null ? "" : String(val);
  return '<div class="field field-wide"><label for="' + id + '">' + label + '</label>' +
    '<textarea id="' + id + '" class="input mono" rows="' + rows + '">' + esc(v) + "</textarea></div>";
}
