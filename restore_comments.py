import sys
import os
import re
import traceback
import subprocess

def restore_comments(file_path):
    print(f"--- Processing {file_path} ---")
    sys.stdout.flush()
    try:
        # 使用 git show 获取 HEAD 内容
        try:
            head_data = subprocess.check_output(['git', 'show', f'HEAD:{file_path}'], stderr=subprocess.STDOUT)
            head_content = head_data.decode('utf-8', errors='ignore').splitlines()
        except subprocess.CalledProcessError as e:
            print(f"Git error: {e.output.decode('utf-8', errors='ignore')}")
            return
        
        # 读取当前文件
        if not os.path.exists(file_path):
            print(f"File not found: {file_path}")
            return
            
        with open(file_path, 'rb') as f:
            curr_data = f.read()
        current_content = curr_data.decode('utf-8', errors='ignore').splitlines()
        
        print(f"Lines in HEAD: {len(head_content)}, Lines in current: {len(current_content)}")
        sys.stdout.flush()
    except Exception as e:
        print(f"Error reading {file_path}: {e}")
        traceback.print_exc()
        return

    # 正则：非贪婪匹配代码部分，然后匹配 // 直到行尾
    comment_pattern = re.compile(r'^(.*?)(\s*//.*)$')

    new_content = []
    
    limit = min(len(head_content), len(current_content))
    
    for i in range(limit):
        head_line = head_content[i]
        curr_line = current_content[i]
        
        head_match = comment_pattern.match(head_line)
        curr_match = comment_pattern.match(curr_line)
        
        if head_match and curr_match:
            head_code, head_comm = head_match.groups()
            curr_code, curr_comm = curr_match.groups()
            
            if head_code.strip() == curr_code.strip():
                new_content.append(head_line)
            else:
                code_part = curr_code.rstrip()
                new_content.append(code_part + head_comm)
        elif head_match and not curr_match:
            if any(ord(c) > 127 for c in curr_line) or '//' in curr_line:
                if '//' in curr_line:
                    code_part = curr_line.split('//')[0]
                    new_content.append(code_part + head_match.group(2))
                else:
                    if head_line.strip().startswith('//'):
                        new_content.append(head_line)
                    else:
                        new_content.append(curr_line)
            else:
                new_content.append(curr_line)
        else:
            new_content.append(curr_line)
            
    if len(current_content) > len(head_content):
        new_content.extend(current_content[len(head_content):])
        
    try:
        with open(file_path, 'w', encoding='utf-8') as f:
            f.write('\n'.join(new_content) + '\n')
        print(f"Successfully restored comments in {file_path}")
    except Exception as e:
        print(f"Error writing {file_path}: {e}")
    sys.stdout.flush()

if __name__ == "__main__":
    files = sys.argv[1:]
    for f in files:
        restore_comments(f)
